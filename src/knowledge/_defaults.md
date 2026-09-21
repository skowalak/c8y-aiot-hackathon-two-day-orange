# Device Type: Defaults

Used when no device-type-specific knowledge file exists. Reason conservatively.

## Expected Measurements
- No fixed ranges known. Treat a series as suspicious only if it is flat-lined
  (identical value across the whole window), zero where a nonzero value is
  expected, or shows a large step change between pass 1 and pass 2.
- Sampling gaps larger than 3× the median inter-sample interval are suspicious.

## Expected Alarms
- Any CRITICAL, unresolved (ACTIVE/ACKNOWLEDGED) alarm = device considered out of service.
- Alarms that flap (repeatedly ACTIVE then CLEARED for the same type) indicate a
  sensor or threshold problem rather than a genuine physical fault.

## Expected Events
- A steady, low rate of routine events is normal.
- Bursts of restart/reboot/error events indicate instability.

## Known Failure Signatures
- Values out of physically plausible bounds that persist across both passes → measurement domain.
- Flapping same-type alarms → alarm domain.
- Repeated restart/error event bursts → event domain.
