Ticket text (no solution hinted):

> Device sim-node-01 ("Power Node 01") has been dropping off intermittently for about a week. The technician on site reports the controller "just restarts by itself" —
most recently this morning, when it was unreachable for several minutes. Nothing looks wrong with the unit itself; the cabinet was inspected and the wiring is tight.
Please analyse what happened around the last incident and tell us whether we need to swap the hardware, roll back the firmware update from last week, or escalate
elsewhere.

Variant with an explicit timestamp for the tool call:

> Fault reported on device sim-node-01 at <RFC3339 fault timestamp>: unexpected restart and loss of connectivity, recurring intermittently over the past 7 days. Root
cause unknown. Produce a diagnostic briefing.

Both give the agent only a device, a time and a symptom — the feeder correlation, the neighbours and the firmware red herring have to be discovered from the data.

## Expected behaviour

Nominal operating envelope of a SIM-PN-100 Power Node, i.e. what "healthy" looks like for this device class:

- Reports telemetry continuously at a 60 s interval; no gaps in the measurement series.
- `sim_Voltage.U` stays within the 230 V nominal ±10 % band, i.e. 207–253 V, with only slow load-driven drift over the day.
- `sim_Current.I` follows the daily load curve in a roughly 8–16 A range, without abrupt steps.
- `sim_Temperature.T` stays in the 35–45 °C cabinet range and well below the 52 °C limit.
- Runs without unplanned restarts; uptime is only interrupted by scheduled maintenance or firmware installation.
- Alarms (`sim_UnderVoltage`, `sim_OverTemperature`) are raised only when the configured threshold rules in `sim_Thresholds` are actually breached, and clear again by themselves once the value returns into the band.
- Availability stays "available" against the configured `c8y_RequiredAvailability` response interval.

Observed instead: repeated unplanned restarts, short telemetry gaps and alarms that appear and clear again on their own, without any operator action or site work in between.
