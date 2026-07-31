package view

import (
	"context"
	"time"

	"github.com/meisfrancis/ecis/internal/awsx"
	"github.com/meisfrancis/ecis/internal/ui"
)

// contextSwitchTimeout bounds credential resolution when switching profiles.
// SSO profiles can take a moment; anything longer than this is a hang.
const contextSwitchTimeout = 30 * time.Second

// NewContexts lists the AWS profiles found in the shared config, so switching
// accounts is the same two keystrokes as switching contexts in k9s.
func NewContexts(app *ui.App) *Browser {
	columns := []ui.Column{
		colE("PROFILE"),
		col("ACTIVE"),
	}

	b := NewBrowser(app, "contexts", "Profiles", columns, func(ctx context.Context) ([]ui.Row, error) {
		profiles := awsx.Profiles()
		if len(profiles) == 0 {
			profiles = []string{app.Profile()}
		}
		current := app.Profile()

		rows := make([]ui.Row, 0, len(profiles))
		for _, p := range profiles {
			active := ""
			if p == current {
				active = "✓"
			}
			var r rowBuilder
			r.add(p).addC(active, ui.ColorOK)
			rows = append(rows, r.build(p, p))
		}
		return rows, nil
	})

	// The profile list comes from disk; polling it would be pointless.
	b.Interval = time.Hour
	b.SortKeys = map[rune]string{'N': "PROFILE"}
	b.SetHints([]ui.Hint{{Key: "enter", Desc: "Switch profile"}})
	b.OnEnter = func(row ui.Row) {
		profile, ok := row.Ref.(string)
		if !ok {
			return
		}
		switchContext(app, profile, app.Region())
	}
	return b
}

// NewRegions lists the regions ECS runs in.
func NewRegions(app *ui.App) *Browser {
	columns := []ui.Column{
		colE("REGION"),
		col("ACTIVE"),
	}

	b := NewBrowser(app, "regions", "Regions", columns, func(ctx context.Context) ([]ui.Row, error) {
		current := app.Region()

		regions := awsx.Regions()
		rows := make([]ui.Row, 0, len(regions))
		for _, region := range regions {
			active := ""
			if region == current {
				active = "✓"
			}
			var r rowBuilder
			r.add(region).addC(active, ui.ColorOK)
			rows = append(rows, r.build(region, region))
		}
		return rows, nil
	})

	b.Interval = time.Hour
	b.SortKeys = map[rune]string{'N': "REGION"}
	b.SetHints([]ui.Hint{{Key: "enter", Desc: "Switch region"}})
	b.OnEnter = func(row ui.Row) {
		region, ok := row.Ref.(string)
		if !ok {
			return
		}
		switchContext(app, app.Profile(), region)
	}
	return b
}

// switchContext rebuilds the AWS session and restarts at the cluster list,
// since nothing currently on screen belongs to the new account or region.
func switchContext(app *ui.App, profile, region string) {
	app.Flash().Infof("switching to %s / %s…", profile, region)

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), contextSwitchTimeout)
		defer cancel()

		err := app.SwitchContext(ctx, profile, region)
		app.QueueUpdateDraw(func() {
			if err != nil {
				app.Flash().Err(err)
				return
			}
			app.Reset(NewClusters(app))
			app.Flash().Infof("now on %s / %s", profile, region)
		})
	}()
}
