#include "census-proof.h"
#include <assert.h>
#include <errno.h>
#include <poll.h>
#include <stdio.h>

int main(void) {
  struct census_probe p = {.pid = 123, .trusted_kernel = true,
                           .same_namespace = true, .syscall_errno = ESRCH};
  assert(census_absence(&p) == CENSUS_ENUMERATED_ABSENT);
  p.start = 456;
  assert(census_absence(&p) == CENSUS_INCARNATION_RETIRED);
  const int failures[] = {EINVAL, EPERM, EACCES, ENOSYS, EMFILE, ENFILE, ENOMEM};
  for (unsigned i = 0; i < sizeof failures / sizeof failures[0]; ++i) {
    p.syscall_errno = failures[i];
    assert(census_absence(&p) == CENSUS_UNPROVEN);
  }
  p.syscall_errno = ESRCH;
  p.trusted_kernel = false;
  assert(census_absence(&p) == CENSUS_UNPROVEN);
  p.trusted_kernel = true; p.same_namespace = false;
  assert(census_absence(&p) == CENSUS_UNPROVEN);
  p.same_namespace = true; p.protected_identity = true;
  assert(census_absence(&p) == CENSUS_UNPROVEN);
  p.protected_identity = false; p.flags = 1;
  assert(census_absence(&p) == CENSUS_UNPROVEN);
  p.flags = 0; p.pid = 0;
  assert(census_absence(&p) == CENSUS_UNPROVEN);
  p.pid = 123; p.syscall_errno = 0;
  assert(census_absence(&p) == CENSUS_UNPROVEN);
  p.bound_start = 456; p.live_after_stat = true; p.poll_events = POLLIN;
  assert(census_absence(&p) == CENSUS_INCARNATION_RETIRED);
  p.poll_events = POLLHUP;
  assert(census_absence(&p) == CENSUS_INCARNATION_RETIRED);
  p.poll_events |= POLLNVAL;
  assert(census_absence(&p) == CENSUS_UNPROVEN);
  p.poll_events = POLLIN | POLLERR;
  assert(census_absence(&p) == CENSUS_UNPROVEN);
  p.poll_events = POLLIN; p.bound_start = 789;
  assert(census_absence(&p) == CENSUS_UNPROVEN);
  p.bound_start = 456; p.live_after_stat = false;
  assert(census_absence(&p) == CENSUS_UNPROVEN);
  p.live_after_stat = true; p.start = 0;
  assert(census_absence(&p) == CENSUS_UNPROVEN);
  puts("kernel-proof decision adversaries: PASS");
  return 0;
}
