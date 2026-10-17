# Original FULL birth and initial-poll companion (#142)

This proposal extends only the existing `bound_seal_birth_terminal` compound
witness. It does not extend standalone zombie/absence, original reaped custody,
whole-observation retry, provider admission or any resource/time allowance.

The producer may link one original FULL NONMANAGED seal birth and at most one
companion, either the existing pass1 ENV failure or a new exact
`process_unavailable / pidfd_poll / ESTALE / null start` fault without a capture
diagnostic. It must independently validate the original birth's retained stage3
pidfd/procdir and the poll companion's OWN retained stage2 pidfd/procdir using
`census_exit_original_full_zombie`. The latter custody is obtained only by the
actual initial-poll capture path; raw ESTALE, an old FULL row or another frame
cannot synthesize it. Both checks require genuine POLLIN, repeated same-start
zombie/stat/UID/fdinfo checks and the same immutable full prior identity.

The existing witness retains `initial_poll=zero`: that describes the original
successful FULL capture, not the companion poll. C, Go and Cloud accept the new
raw companion only inside this same group, with one birth link, at most one
companion link total and the same classified birth scan. The producer's compound
witness attests both descriptor checks; no new wire keys or proof kinds are added.
Existing ENV diagnostic and independent-descriptor checks remain unchanged.

The 21:58 saved reply is diagnostic evidence, not a positive test. It contains
FULL rows in scans4–6 and an unresolved poll/null fault in scan7, but does not
serialize revents, retained custody or readable zombie state. It may remain
UNKNOWN even with this patch. Separate managed-descendant churn, protected
identity/provider joins and COMPLETE/whole eligibility fences remain mandatory.
All occupied retention16 slots remain immutable.

Local proof layers are deliberately distinct:

- Portable tests compile the actual resolver against finite owned witness-boundary
  stubs. They test selection, independent calls, refusal and the one-companion
  bound, not kernel truth.
- The shared modeled JSON vector tests C-shaped wire validation in Go and Cloud.
  It is explicitly not a new Linux capture.
- `census-original-full-poll-kernel-test.c` uses only two owned env-empty children
  and controlled enumeration. Production capture obtains real original FULL
  stage3 in scan4, real FULL rows5–6 and real initial POLLIN/stage2 in scan7 while
  that numeric PID remains present. It checks raw preservation, both descriptor
  checks, a future independent closing triplet and the actual final C validator.
  It also denies missing stage2, UID/start changes, a live foreign child's fd and
  a reaped original. No host process enumeration, observer socket, RPC, database,
  production city or business command is invoked.

The same new kernel fixture must be compiled against archived C28 headers for
old-RED and candidate headers for GREEN. Old-RED counts only after real stage2 is
obtained and a separate stage2 resolution is possible while the shared birth
group is refused. Bootstrap/ABI/FD errors are test failures, not the intended RED.
The actual produced vector must then pass both consumers. Linux execution and
its exact kernel/architecture remain required; local portable tests cannot
substitute for that boundary or establish a production 6.8 profile.

Canonical policy change is tracked in the separate gascity-kb Source proposal
`VPS-OPS-CAPACITY-01.md#stage3-poll-companion`. Owner approval, coherent Source,
consumer/policy pins, normal gates, Linux PAIR and guarded delivery remain pending.
Nothing in this proposal authorizes Host apply or another Native admission.
