package core

import (
	"context"
	"path/filepath"
	"reflect"

	"github.com/hashicorp/go-argmapper"
	"github.com/hashicorp/go-hclog"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/hashicorp/waypoint-plugin-sdk/component"
	"github.com/hashicorp/waypoint-plugin-sdk/datadir"
	"github.com/hashicorp/waypoint-plugin-sdk/terminal"
	"github.com/hashicorp/waypoint/internal/config2"
	"github.com/hashicorp/waypoint/internal/factory"
	pb "github.com/hashicorp/waypoint/internal/server/gen"
)

// App represents a single application and exposes all the operations
// that can be performed on an application.
//
// An App is only valid if it was returned by Project.App. The behavior of
// App if constructed in any other way is undefined and likely to result
// in crashes.
type App struct {
	// UI is the UI that should be used for any output that is specific
	// to this app vs the project UI.
	UI terminal.UI

	project   *Project
	config    *config.App
	ref       *pb.Ref_Application
	workspace *pb.Ref_Workspace
	client    pb.WaypointClient
	source    *component.Source
	jobInfo   *component.JobInfo
	logger    hclog.Logger
	dir       *datadir.App
	mappers   []*argmapper.Func
	closers   []func() error
}

type appComponent struct {
	// Info is the protobuf metadata for this component.
	Info *pb.Component

	// Dir is the data directory for this component.
	Dir *datadir.Component

	// Labels are the set of labels that were set for this component.
	// This isn't merged yet with parent labels and app.mergeLabels must
	// be called.
	Labels map[string]string

	// Hooks are the hooks associated with this component keyed by their When value
	Hooks map[string][]*config.Hook
}

// newApp creates an App for the given project and configuration. This will
// initialize and configure all the components of this application. An error
// will be returned if this app fails to initialize: configuration is invalid,
// a component could not be found, etc.
func newApp(
	ctx context.Context,
	p *Project,
	cfg *config.App,
) (*App, error) {
	// Initialize
	app := &App{
		project: p,
		client:  p.client,
		source:  &component.Source{App: cfg.Name, Path: "."},
		jobInfo: p.jobInfo,
		logger:  p.logger.Named("app").Named(cfg.Name),
		ref: &pb.Ref_Application{
			Application: cfg.Name,
			Project:     p.name,
		},
		workspace: p.WorkspaceRef(),
		config:    cfg,

		// very important below that we allocate a new slice since we modify
		mappers: append([]*argmapper.Func{}, p.mappers...),

		// set the UI, which for now is identical to project but in the
		// future should probably change as we do app-scoping, parallelization,
		// etc.
		UI: p.UI,
	}

	// Determine our path
	path := p.root
	if cfg.Path != "" {
		path = filepath.Join(path, cfg.Path)
	}
	app.source.Path = path

	// Setup our directory
	dir, err := p.dir.App(cfg.Name)
	if err != nil {
		return nil, err
	}
	app.dir = dir

	// Initialize mappers if we have those
	if f, ok := p.factories[component.MapperType]; ok {
		err = app.initMappers(ctx, f)
		if err != nil {
			return nil, err
		}
	}

	// If we don't have a releaser but our platform implements release then
	// we use that.
	/* TODO(config2)
	if app.Releaser == nil && app.Platform != nil {
		app.logger.Trace("no releaser configured, checking if platform supports release")
		if r, ok := app.Platform.(component.PlatformReleaser); ok && r.DefaultReleaserFunc() != nil {
			app.logger.Info("platform capable of release, using platform for release")
			raw, err := app.callDynamicFunc(
				ctx,
				app.logger,
				(*component.ReleaseManager)(nil),
				app.Platform,
				r.DefaultReleaserFunc(),
			)
			if err != nil {
				return nil, err
			}

			app.Releaser = raw.(component.ReleaseManager)
			app.components[app.Releaser] = app.components[app.Platform]
		} else {
			app.logger.Info("no releaser configured, platform does not support a default releaser",
				"platform_type", fmt.Sprintf("%T", app.Platform),
			)
		}
	}
	*/

	return app, nil
}

// Close is called to clean up any resources. This should be called
// whenever the app is done being used. This will be called by Project.Close.
func (a *App) Close() error {
	for _, c := range a.closers {
		c()
	}

	return nil
}

// Ref returns the reference to this application for us in API calls.
func (a *App) Ref() *pb.Ref_Application {
	return a.ref
}

/* TODO(config2)
// Components returns the list of components that were initilized for this app.
func (a *App) Components() []interface{} {
	var result []interface{}
	for c := range a.components {
		result = append(result, c)
	}

	return result
}

// ComponentProto returns the proto info for a component. The passed component
// must be part of the app or nil will be returned.
func (a *App) ComponentProto(c interface{}) *pb.Component {
	info, ok := a.components[c]
	if !ok {
		return nil
	}

	return info.Info
}
*/

// mergeLabels merges the set of labels given. See project.mergeLabels.
// This is the app-specific version that adds the proper app-specific labels
// as necessary.
func (a *App) mergeLabels(ls ...map[string]string) map[string]string {
	ls = append([]map[string]string{a.config.Labels}, ls...)
	return a.project.mergeLabels(ls...)
}

// callDynamicFunc calls a dynamic function which is a common pattern for
// our component interfaces. These are functions that are given to mapper,
// supplied with a series of arguments, dependency-injected, and then called.
//
// This always provides some common values for injection:
//
//   * *component.Source
//   * *datadir.Project
//   * history.Client
//
func (a *App) callDynamicFunc(
	ctx context.Context,
	log hclog.Logger,
	result interface{}, // expected result type
	c *Component, // component
	f interface{}, // function
	args ...argmapper.Arg,
) (interface{}, error) {
	// We allow f to be a *mapper.Func because our plugin system creates
	// a func directly due to special argument types.
	// TODO: test
	rawFunc, ok := f.(*argmapper.Func)
	if !ok {
		var err error
		rawFunc, err = argmapper.NewFunc(f, argmapper.Logger(log))
		if err != nil {
			return nil, err
		}
	}

	// Be sure that the status is closed after every operation so we don't leak
	// weird output outside the normal execution.
	defer a.UI.Status().Close()

	// Make sure we have access to our context and logger and default args
	args = append(args,
		argmapper.ConverterFunc(a.mappers...),
		argmapper.Typed(
			ctx,
			log,
			a.source,
			a.jobInfo,
			a.dir,
			a.UI,
		),

		argmapper.Named("labels", &component.LabelSet{Labels: c.labels}),
	)

	// Build the chain and call it
	callResult := rawFunc.Call(args...)
	if err := callResult.Err(); err != nil {
		return nil, err
	}
	raw := callResult.Out(0)

	// If we don't have an expected result type, then just return as-is.
	// Otherwise, we need to verify the result type matches properly.
	if result == nil {
		return raw, nil
	}

	// Verify
	interfaceType := reflect.TypeOf(result).Elem()
	if rawType := reflect.TypeOf(raw); !rawType.Implements(interfaceType) {
		return nil, status.Errorf(codes.FailedPrecondition,
			"operation expected result type %s, got %s",
			interfaceType.String(),
			rawType.String())
	}

	return raw, nil
}

// initMappers initializes plugins that are just mappers.
func (a *App) initMappers(
	ctx context.Context,
	f *factory.Factory,
) error {
	log := a.logger

	for _, name := range f.Registered() {
		log.Debug("loading mapper plugin", "name", name)

		// Start the component
		pinst, err := a.startPlugin(ctx, component.MapperType, f, name)
		if err != nil {
			return err
		}

		// We store the mappers
		a.mappers = append(a.mappers, pinst.Mappers...)

		// Add this to our closer list
		a.closers = append(a.closers, func() error {
			pinst.Close()
			return nil
		})
	}

	return nil
}
