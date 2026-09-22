package sim

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/skowalak/c8y-aiot-hackathon-two-day-orange/simulator/internal/c8y"
)

// collections purged per node, in the order they are deleted.
var collections = []string{"measurement/measurements", "alarm/alarms", "event/events"}

// PurgeOptions bounds a purge.
type PurgeOptions struct {
	// From and To bound the deletion. The Cumulocity bulk delete endpoints
	// refuse a request without a date range, and the measurement endpoint
	// additionally rejects bounds that are not truncated to the hour, so Purge
	// widens the range outwards to whole hours.
	From, To time.Time
	// Workers is the number of nodes purged in parallel.
	Workers int
	// DryRun only reports what is there without deleting.
	DryRun bool
}

// Purge deletes the telemetry, alarms and events of the fleet so the scenario
// can be seeded again from scratch. Devices, groups, inventory fragments and
// external IDs are deliberately kept: re-running the backfill then reuses the
// same managed objects instead of creating a second copy of the fleet, and any
// report or dashboard that references a device ID keeps working.
//
// Nodes that do not exist in the tenant are skipped rather than created; a
// destructive operation should not provision anything.
//
// Bulk deletion is asynchronous on the platform side: the DELETE returns 204
// while the records are still being removed, so a single pass reliably leaves a
// residue of a few dozen measurements behind. Purge therefore repeats
// delete-and-verify until the fleet counts stop dropping.
//
// A live simulator publishing at the same time will repopulate the tenant
// immediately, so stop it first. Purge reports what is left afterwards to make
// that visible.
func Purge(ctx context.Context, client *c8y.Client, nodes []*Node, opt PurgeOptions) error {
	if opt.To.IsZero() {
		opt.To = time.Now().Add(time.Hour)
	}
	if opt.From.IsZero() {
		opt.From = opt.To.AddDate(-1, 0, 0)
	}
	// DELETE /measurement/measurements answers 422 "Date from and date to
	// parameters used for delete filter must be truncated to hour!" otherwise.
	// Rounding From down and To up only ever widens the window, so nothing that
	// the caller asked to delete survives.
	opt.From = opt.From.Truncate(time.Hour)
	opt.To = opt.To.Truncate(time.Hour).Add(time.Hour)
	if !opt.From.Before(opt.To) {
		return fmt.Errorf("purge: empty range %s..%s", opt.From, opt.To)
	}
	if opt.Workers <= 0 {
		opt.Workers = 4
	}

	present, err := resolve(ctx, client, nodes)
	if err != nil {
		return err
	}
	if len(present) == 0 {
		slog.Warn("purge: no simulator devices found in the tenant, nothing to do")
		return nil
	}
	slog.Info("purging", "devices", len(present),
		"from", opt.From.UTC().Format(time.RFC3339), "to", opt.To.UTC().Format(time.RFC3339),
		"dryRun", opt.DryRun)

	remaining, err := sweep(ctx, client, present, opt)
	if err != nil {
		return err
	}
	if opt.DryRun {
		slog.Info("purge dry run complete", "itemsFound", remaining)
		return nil
	}

	// Chase the asynchronous deletion. Progress, not a fixed number of rounds,
	// is the stop condition: a count that no longer drops means either the
	// tenant is clean or something is writing faster than we delete.
	for round := 2; remaining > 0 && round <= maxSweeps; round++ {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(sweepDelay):
		}
		slog.Info("residue left by asynchronous deletion, sweeping again",
			"round", round, "itemsLeft", remaining)
		left, err := sweep(ctx, client, present, opt)
		if err != nil {
			return err
		}
		if left >= remaining {
			remaining = left
			break
		}
		remaining = left
	}

	if remaining > 0 {
		slog.Warn("purge finished but data remains; a live simulator is probably still publishing",
			"itemsLeft", remaining)
	} else {
		slog.Info("purge complete: fleet telemetry, alarms and events removed, devices kept")
	}
	return nil
}

const (
	// maxSweeps bounds the delete-and-verify rounds so a fleet that keeps
	// receiving data cannot spin here forever.
	maxSweeps = 6
	// sweepDelay gives the platform's deletion job time to catch up before the
	// counts are believed.
	sweepDelay = 15 * time.Second
)

// sweep purges every node once, in parallel, and returns the total number of
// items still counted afterwards.
func sweep(ctx context.Context, client *c8y.Client, nodes []*Node, opt PurgeOptions) (int, error) {
	var (
		mu        sync.Mutex
		remaining int
		firstErr  error
	)
	sem := make(chan struct{}, opt.Workers)
	var wg sync.WaitGroup
	for _, n := range nodes {
		wg.Add(1)
		go func(n *Node) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			left, err := purgeNode(ctx, client, n, opt)
			mu.Lock()
			defer mu.Unlock()
			remaining += left
			if err != nil && firstErr == nil {
				firstErr = err
			}
		}(n)
	}
	wg.Wait()
	return remaining, firstErr
}

func purgeNode(ctx context.Context, client *c8y.Client, n *Node, opt PurgeOptions) (int, error) {
	for _, coll := range collections {
		before, err := client.Count(ctx, coll, n.ID, opt.From, opt.To)
		if err != nil {
			return 0, fmt.Errorf("purge %s %s: %w", n.Serial, coll, err)
		}
		if before == 0 {
			continue
		}
		if opt.DryRun {
			slog.Info("would delete", "device", n.Name, "collection", coll, "items", before)
			continue
		}
		if err := deleteCollection(ctx, client, coll, n.ID, opt.From, opt.To); err != nil {
			return 0, fmt.Errorf("purge %s %s: %w", n.Serial, coll, err)
		}
		slog.Info("deleted", "device", n.Name, "collection", coll, "items", before)
	}

	// Verify, because a concurrently running live simulator makes a purge look
	// successful while the tenant refills behind it.
	var left int
	for _, coll := range collections {
		got, err := client.Count(ctx, coll, n.ID, opt.From, opt.To)
		if err != nil {
			return left, fmt.Errorf("purge verify %s %s: %w", n.Serial, coll, err)
		}
		left += got
	}
	return left, nil
}

func deleteCollection(ctx context.Context, client *c8y.Client, collection, sourceID string, from, to time.Time) error {
	switch collection {
	case "measurement/measurements":
		return client.DeleteMeasurements(ctx, sourceID, from, to)
	case "alarm/alarms":
		return client.DeleteAlarms(ctx, sourceID, from, to)
	case "event/events":
		return client.DeleteEvents(ctx, sourceID, from, to)
	default:
		return fmt.Errorf("unknown collection %q", collection)
	}
}

// resolve fills in the managed object IDs of the nodes that already exist,
// leaving the tenant unchanged.
func resolve(ctx context.Context, client *c8y.Client, nodes []*Node) ([]*Node, error) {
	var out []*Node
	for _, n := range nodes {
		id, err := client.LookupExternalID(ctx, c8y.ExternalIDTypeSerial, n.Serial)
		if err != nil {
			if c8y.NotFound(err) {
				slog.Debug("device not in tenant, skipping", "serial", n.Serial)
				continue
			}
			return nil, fmt.Errorf("resolve %s: %w", n.Serial, err)
		}
		n.ID = id
		out = append(out, n)
	}
	return out, nil
}
