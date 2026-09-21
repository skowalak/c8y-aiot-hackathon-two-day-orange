# Device Type: Water Pump

## Expected Measurements
- c8y_Pressure (bar): normal 2.0–6.0, warning <1.5 or >7.0, critical <1.0 or >8.0
- c8y_FlowRate (l/min): normal 20–120; zero flow while pump ON = fault
- Sampling interval: every 60s; gaps >5 min are suspicious

## Expected Alarms
- c8y_MaintenanceDue (MINOR) is routine — ignore for fault classification
- Any CRITICAL, unresolved alarm = device considered out of service

## Expected Events
- c8y_PumpStarted / c8y_PumpStopped pairs are normal
- Repeated c8y_PumpStopped without a start = suspected event-domain fault

## Known Failure Signatures
- Pressure normal but flow zero → mechanical blockage (measurement domain)
- Flapping ACTIVE/CLEARED alarms of same type → sensor/threshold fault (alarm domain)
- Bursts of c8y_Restart events → firmware instability (event domain)
