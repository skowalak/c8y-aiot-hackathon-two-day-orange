package sim

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/skowalak/c8y-aiot-hackathon-two-day-orange/simulator/internal/c8y"
)

// Fragment, series and type names used across inventory, measurements, alarms
// and events. The MCP diagnostic engine keys off these.
const (
	DeviceType      = "sim_PowerNode"
	MeasurementType = "sim_PowerNodeTelemetry"

	FragmentVoltage = "sim_Voltage"
	SeriesVoltage   = "U"
	FragmentCurrent = "sim_Current"
	SeriesCurrent   = "I"
	FragmentTemp    = "sim_Temperature"
	SeriesTemp      = "T"

	AlarmUnderVoltage   = "sim_UnderVoltage"
	AlarmOverTemp       = "sim_OverTemperature"
	EventSupplyDip      = "sim_SupplyDip"
	EventBrownoutReset  = "sim_BrownoutReset"
	EventFirmwareUpdate = "sim_FirmwareUpdated"
)

// FleetSpec describes the fleet to be provisioned.
type FleetSpec struct {
	Prefix       string // serial prefix, e.g. "sim"
	Neighbors    int    // additional nodes on the affected feeder
	Baseline     int    // nodes on the healthy control feeder
	AffectedSite string
	ControlSite  string
	Firmware     string
	FirmwareOld  string
}

// DefaultSpec returns the demo fleet: four nodes on the failing feeder and two
// healthy control nodes.
func DefaultSpec() FleetSpec {
	return FleetSpec{
		Prefix:       "sim",
		Neighbors:    3,
		Baseline:     2,
		AffectedSite: "SIM Substation Alpha",
		ControlSite:  "SIM Substation Beta",
		Firmware:     "1.4.2",
		FirmwareOld:  "1.4.0",
	}
}

// BuildFleet materialises the node list from the spec. Coupling decays with the
// distance of the node from the primary node on the feeder.
func BuildFleet(spec FleetSpec) []*Node {
	nodes := make([]*Node, 0, 1+spec.Neighbors+spec.Baseline)

	newNode := func(idx int, site, feeder string, role Role, coupling float64) *Node {
		serial := fmt.Sprintf("%s-node-%02d", spec.Prefix, idx)
		n := &Node{
			Serial:      serial,
			Name:        fmt.Sprintf("Power Node %02d (%s)", idx, feeder),
			Site:        site,
			Feeder:      feeder,
			Role:        role,
			Coupling:    coupling,
			Firmware:    spec.Firmware,
			BaseVoltage: NominalVoltage + 1.5*unitNoise(hashString(serial), 7),
			BaseCurrent: 11.5 + 2.5*unitNoise(hashString(serial), 11),
			BaseTemp:    37 + 3*unitNoise(hashString(serial), 13),
			seed:        hashString(serial),
		}
		nodes = append(nodes, n)
		return n
	}

	idx := 1
	newNode(idx, spec.AffectedSite, "Feeder-A", RolePrimary, 1.0)
	for i := range spec.Neighbors {
		idx++
		coupling := 0.82 - 0.14*float64(i)
		if coupling < 0.3 {
			coupling = 0.3
		}
		newNode(idx, spec.AffectedSite, "Feeder-A", RoleNeighbor, round(coupling, 2))
	}
	for range spec.Baseline {
		idx++
		newNode(idx, spec.ControlSite, "Feeder-B", RoleBaseline, 0)
	}
	return nodes
}

// Primary returns the node the fault is reported for.
func Primary(nodes []*Node) *Node {
	for _, n := range nodes {
		if n.Role == RolePrimary {
			return n
		}
	}
	return nodes[0]
}

// Provision creates the device groups, the devices, their external IDs and the
// inventory fragments the diagnostic engine reads (thresholds, topology,
// firmware). It is idempotent: existing devices are reused and updated.
func Provision(ctx context.Context, client *c8y.Client, nodes []*Node, spec FleetSpec) error {
	groups := map[string]string{}
	for _, site := range []string{spec.AffectedSite, spec.ControlSite} {
		if _, ok := groups[site]; ok {
			continue
		}
		id, err := ensureGroup(ctx, client, site)
		if err != nil {
			return err
		}
		groups[site] = id
		slog.Info("group ready", "name", site, "id", id)
	}

	for _, n := range nodes {
		n.GroupID = groups[n.Site]
		created := false
		id, err := client.LookupExternalID(ctx, c8y.ExternalIDTypeSerial, n.Serial)
		switch {
		case err == nil:
			n.ID = id
		case c8y.NotFound(err):
			if n.ID, err = client.CreateManagedObject(ctx, deviceFragments(n, spec)); err != nil {
				return fmt.Errorf("create device %s: %w", n.Serial, err)
			}
			if err := client.BindExternalID(ctx, n.ID, c8y.ExternalIDTypeSerial, n.Serial); err != nil {
				return fmt.Errorf("bind serial %s: %w", n.Serial, err)
			}
			created = true
		default:
			return fmt.Errorf("lookup %s: %w", n.Serial, err)
		}

		if !created {
			if err := client.UpdateManagedObject(ctx, n.ID, deviceFragments(n, spec)); err != nil {
				return fmt.Errorf("update device %s: %w", n.Serial, err)
			}
		}
		if err := client.AddChildAsset(ctx, n.GroupID, n.ID); err != nil {
			return fmt.Errorf("assign %s to group: %w", n.Serial, err)
		}
		slog.Info("device ready", "serial", n.Serial, "id", n.ID, "role", string(n.Role),
			"coupling", n.Coupling, "created", created)
	}
	return nil
}

func ensureGroup(ctx context.Context, client *c8y.Client, name string) (string, error) {
	id, err := client.FindManagedObjectByName(ctx, name, "c8y_DeviceGroup")
	if err != nil {
		return "", fmt.Errorf("find group %s: %w", name, err)
	}
	if id != "" {
		return id, nil
	}
	id, err = client.CreateManagedObject(ctx, map[string]any{
		"name":                name,
		"type":                "c8y_DeviceGroup",
		"c8y_IsDeviceGroup":   map[string]any{},
		"sim_IsSimulatedSite": true,
	})
	if err != nil {
		return "", fmt.Errorf("create group %s: %w", name, err)
	}
	return id, nil
}

func deviceFragments(n *Node, spec FleetSpec) map[string]any {
	return map[string]any{
		"name":                       n.Name,
		"type":                       DeviceType,
		"c8y_IsDevice":               map[string]any{},
		"com_cumulocity_model_Agent": map[string]any{},
		"c8y_Hardware": map[string]any{
			"model":        "SIM-PN-100",
			"revision":     "B2",
			"serialNumber": n.Serial,
		},
		"c8y_Firmware": map[string]any{
			"name":    "powernode-fw",
			"version": spec.Firmware,
			"url":     "https://example.invalid/fw/powernode-" + spec.Firmware + ".bin",
		},
		"c8y_RequiredAvailability": map[string]any{"responseInterval": 5},
		"c8y_SupportedMeasurements": []string{
			FragmentVoltage, FragmentCurrent, FragmentTemp,
		},
		// Active threshold rules, mirrored into inventory so the diagnostic
		// engine can report which rule fired and why.
		"sim_Thresholds": map[string]any{
			FragmentVoltage + "." + SeriesVoltage: map[string]any{
				"min": UnderVoltageV, "max": 253.0, "severity": c8y.SeverityMajor,
				"alarmType": AlarmUnderVoltage, "unit": "V",
			},
			FragmentTemp + "." + SeriesTemp: map[string]any{
				"max": CriticalTempC, "severity": c8y.SeverityCritical,
				"alarmType": AlarmOverTemp, "unit": "C",
			},
		},
		"sim_Topology": map[string]any{
			"site":     n.Site,
			"feeder":   n.Feeder,
			"role":     string(n.Role),
			"coupling": n.Coupling,
		},
	}
}
