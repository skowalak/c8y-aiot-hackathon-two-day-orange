Ticket text (no solution hinted):

> Device sim-node-01 ("Power Node 01") has been dropping off intermittently for about a week. The technician on site reports the controller "just restarts by itself" —
most recently this morning, when it was unreachable for several minutes. Nothing looks wrong with the unit itself; the cabinet was inspected and the wiring is tight.
Please analyse what happened around the last incident and tell us whether we need to swap the hardware, roll back the firmware update from last week, or escalate
elsewhere.

Variant with an explicit timestamp for the tool call:

> Fault reported on device sim-node-01 at <RFC3339 fault timestamp>: unexpected restart and loss of connectivity, recurring intermittently over the past 7 days. Root
cause unknown. Produce a diagnostic briefing.                                                                                                                          ▐
                                                                                                                                                                       ▐
Both give the agent only a device, a time and a symptom — the feeder correlation, the neighbours and the firmware red herring have to be discovered from the data.     ▐

