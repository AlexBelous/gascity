#ifndef GC_CENSUS_PROOF_H
#define GC_CENSUS_PROOF_H

#include <errno.h>
#include <poll.h>
#include <stdbool.h>
#include <stddef.h>
#include <stdint.h>

/* These facts are supplied by the syscall adapter, never by the requester.
 * ESRCH from procfs, fixtures, ptrace or an unreviewed seccomp filter is not a
 * trusted kernel result. Linux 6.8 pidfd_open flags=0 is the reviewed ABI. */
struct census_probe {
  uint32_t pid, flags;
  uint64_t start, bound_start;
  int syscall_errno, poll_events;
  bool trusted_kernel, same_namespace, live_after_stat, protected_identity;
};
enum census_absence_kind {
  CENSUS_UNPROVEN = 0,
  CENSUS_ENUMERATED_ABSENT,
  CENSUS_INCARNATION_RETIRED,
  CENSUS_TERMINAL_ZOMBIE,
  CENSUS_UNCLASSIFIED_INCARNATION_RETIRED
};
static inline const char *census_proof_kind_name(enum census_absence_kind kind) {
  switch (kind) {
  case CENSUS_ENUMERATED_ABSENT: return "enumerated_pid_absent";
  case CENSUS_INCARNATION_RETIRED: return "incarnation_retired";
  case CENSUS_TERMINAL_ZOMBIE: return "terminal_zombie";
  case CENSUS_UNCLASSIFIED_INCARNATION_RETIRED: return "unclassified_incarnation_retired";
  default: return NULL;
  }
}
static enum census_absence_kind census_absence(const struct census_probe *p) {
  if (!p->pid || p->pid > INT32_MAX || p->flags || !p->trusted_kernel ||
      !p->same_namespace || p->protected_identity)
    return CENSUS_UNPROVEN;
  /* Linux kernel/pid.c: this exact syscall error precedes pidfd allocation
   * and means find_get_pid() found no PID in the caller's active namespace. */
  if (p->syscall_errno == ESRCH)
    return p->start ? CENSUS_INCARNATION_RETIRED : CENSUS_ENUMERATED_ABSENT;
  if (p->syscall_errno || !p->start || p->start != p->bound_start ||
      !p->live_after_stat || (p->poll_events & (POLLERR | POLLNVAL)) ||
      !(p->poll_events & (POLLIN | POLLHUP)))
    return CENSUS_UNPROVEN;
  return CENSUS_INCARNATION_RETIRED;
}
#endif
