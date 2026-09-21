# Device Type: Power Node (SIM-PN-100)

Nominal operating envelope of a SIM-PN-100 Power Node — what "healthy" looks
like for this device class. Source: TICKET.md "Expected behaviour".

## Expected Measurements
- Reports telemetry continuously at a **60 s interval** — no gaps in the series.
  Gaps in the measurement series indicate the controller restarted / lost
  connectivity (unexpected downtime).
- `sim_Voltage.U` (V): stays within the 230 V nominal **±10 %**, i.e. **207–253 V**,
  with only slow load-driven drift over the day. Dips below 207 V are under-voltage.
- `sim_Current.I` (A): follows the daily load curve in a roughly **8–16 A** range,
  **without abrupt steps**. Note: constant-power loads draw *more* current when
  voltage sags — a current spike coinciding with a voltage dip points at a supply
  problem, not the unit.
- `sim_Temperature.T` (°C): stays in the **35–45 °C** cabinet range and well below
  the **52 °C** limit.

## Expected Alarms
- Only `sim_UnderVoltage` and `sim_OverTemperature` are expected, and only when the
  configured threshold rules in `sim_Thresholds` are **actually breached**.
- Healthy behaviour: an alarm clears again **by itself** once the value returns
  into the band. Alarms that **appear and self-clear repeatedly** without any
  operator action or site work in between are the fault signature, not routine.
- Under-voltage thresholds: min 207 V (MAJOR); temperature limit 52 °C (CRITICAL).

## Expected Events
- Routine firmware/maintenance events are normal and planned.
- `sim_BrownoutReset` events = **unplanned controller restarts**. These are NOT
  expected in healthy operation; repeated ones are the core symptom.
- `sim_SupplyDip` events mark feeder voltage dips.
- A one-off `sim_FirmwareUpdated` event around the onset is a common **red herring**
  — do not attribute the fault to firmware unless the restarts line up with the
  firmware change rather than with the voltage dips.

## Availability
- Stays "available" against the configured `c8y_RequiredAvailability` response
  interval. Going unavailable = missed telemetry = a restart/outage.

## Known Failure Signatures
- **Repeated unplanned restarts** (`sim_BrownoutReset` events) + **short telemetry
  gaps** + **self-clearing `sim_UnderVoltage` alarms**, with voltage sagging below
  207 V and current spiking at the same instants → **supply/feeder brownout**
  (event + alarm domain), NOT a hardware fault of the unit.
- If restarts coincide with voltage dips (not with the firmware event), the
  firmware rollback is a distraction; the cause is the electrical supply.
- Temperature staying in-band during the incidents rules out thermal shutdown.
