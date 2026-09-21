package sim

import (
	"encoding/csv"
	"fmt"
	"io"
)

// RegistrationCSV writes a Cumulocity *full* bulk device registration file for
// the fleet (Device management > Devices > Registration > Register device >
// Bulk registration > General).
//
// Cumulocity derives the device user name from the ID column as
// <tenant>/device_<ID>, with the CREDENTIALS column as password. Run the
// simulator with -device-password <password> to make the live MQTT phase use
// those device credentials instead of the tenant user.
func RegistrationCSV(w io.Writer, nodes []*Node, spec FleetSpec, password string) error {
	if password == "" {
		return fmt.Errorf("registration csv: a device password is required")
	}

	cw := csv.NewWriter(w)
	cw.Comma = ';'
	rows := [][]string{{"ID", "CREDENTIALS", "TYPE", "NAME", "IDTYPE", "PATH", "SHELL", "AUTH_TYPE"}}
	for _, n := range nodes {
		rows = append(rows, []string{
			n.Serial,
			password,
			DeviceType,
			n.Name,
			"c8y_Serial",
			n.Site, // group path, created if missing
			"0",
			"BASIC",
		})
	}
	if err := cw.WriteAll(rows); err != nil {
		return fmt.Errorf("registration csv: %w", err)
	}
	cw.Flush()
	return cw.Error()
}
