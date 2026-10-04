/* Linux-only, one connection, one observation. No subprocesses or signal sends.
 */
#define _GNU_SOURCE
#include "sha256.h"
#include <arpa/inet.h>
#include <errno.h>
#include <fcntl.h>
#include <inttypes.h>
#include <linux/audit.h>
#include <linux/filter.h>
#include <linux/magic.h>
#include <linux/openat2.h>
#include <linux/seccomp.h>
#include <poll.h>
#include <signal.h>
#include <stdarg.h>
#include <stdbool.h>
#include <stddef.h>
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/prctl.h>
#include <sys/socket.h>
#include <sys/stat.h>
#include <sys/statfs.h>
#include <sys/syscall.h>
#include <sys/types.h>
#include <time.h>
#include <unistd.h>
#ifndef HELPER_SOURCE_REVISION
#error HELPER_SOURCE_REVISION must pin the exact source commit
#endif
#if !defined(__x86_64__)
#error Only the reviewed Linux x86_64 ABI is supported
#endif
#define CONNECTION_FD 3
#if defined(GC_HELPER_TEST) && defined(TEST_MAX_PIDS)
#define MAX_PIDS TEST_MAX_PIDS
#else
#define MAX_PIDS 65536u
#endif
#define MAX_ENV (16u * 1024u * 1024u)
#define MAX_OUTPUT (16u * 1024u * 1024u)
#define MAX_RETAINED (16u * 1024u * 1024u)
#define MAX_ERRORS 128u
#define MAX_REQUEST 1024u
#define MAX_FIELD 4096u
#define POLICY                                                                 \
  "gc-proc-helper/"                                                            \
  "v1;fd=3;request=1024;wall_ms=10000;pids=65536;env=16777216;output="         \
  "16777216;retained=16777216;errors=128;openat2=beneath,no_symlinks,no_"      \
  "magiclinks;seccomp=default_errno_x86_64_v2;roots=exact_tmux_v1"
struct binding {
  uint32_t pid, uid;
  uint64_t start;
  char boot[37], ns[80], source[41], binary[65], helper_binary[65];
};
struct process {
  uint32_t pid, ppid, pgid;
  uint64_t start, epoch;
  char *sid, *city, *template, *name;
  char token[65];
  bool valid, root, parent_infra;
};
struct scan {
  struct process *p;
  size_t n, retained;
  char digest[65];
};
struct error_item {
  const char *reason, *operation;
  uint32_t pid;
  int error;
};
struct evidence {
  struct binding binding;
  struct scan before, after;
  struct error_item errors[MAX_ERRORS];
  size_t errors_n, errors_total;
  char nonce[65], boot[37], ns[80], binary[65], policy[65], started[40],
      finished[40];
  struct timespec monotonic_start, realtime_start;
  uint64_t duration;
  int procfd;
  bool complete;
};
static struct evidence ev;
static const char *proc_path = "/proc",
                  *binding_path = "/etc/gascity-observer/binding.conf";
static bool fixture = false;
static void add_error(const char *reason, const char *op, uint32_t pid,
                      int error) {
  ev.complete = false;
  ev.errors_total++;
  if (ev.errors_n < MAX_ERRORS)
    ev.errors[ev.errors_n++] = (struct error_item){reason, op, pid, error};
}
static uint64_t mono_ms(void) {
  struct timespec t;
  if (clock_gettime(CLOCK_MONOTONIC, &t))
    return UINT64_MAX;
  return (uint64_t)t.tv_sec * 1000u + (uint64_t)t.tv_nsec / 1000000u;
}
static bool expired(void) {
  uint64_t start = (uint64_t)ev.monotonic_start.tv_sec * 1000u +
                   (uint64_t)ev.monotonic_start.tv_nsec / 1000000u;
  return mono_ms() - start >= 10000u;
}
static bool hex_string(const char *s, size_t n) {
  if (strlen(s) != n)
    return false;
  for (size_t i = 0; i < n; i++)
    if (!((s[i] >= '0' && s[i] <= '9') || (s[i] >= 'a' && s[i] <= 'f')))
      return false;
  return true;
}
static bool uuid_string(const char *s) {
  if (strlen(s) != 36)
    return false;
  for (size_t i = 0; i < 36; i++) {
    if (i == 8 || i == 13 || i == 18 || i == 23) {
      if (s[i] != '-')
        return false;
    } else if (!((s[i] >= '0' && s[i] <= '9') || (s[i] >= 'a' && s[i] <= 'f')))
      return false;
  }
  return true;
}
static bool namespace_string(const char *s) {
  size_t n = strlen(s);
  if (n < 7 || n >= 80 || strncmp(s, "pid:[", 5) || s[n - 1] != ']')
    return false;
  for (size_t i = 5; i < n - 1; i++)
    if (s[i] < '0' || s[i] > '9')
      return false;
  return true;
}
static bool uint_value(const char *s, uint64_t *v) {
  if (!*s)
    return false;
  uint64_t x = 0;
  for (; *s; s++) {
    if (*s < '0' || *s > '9' || x > (UINT64_MAX - (unsigned)(*s - '0')) / 10)
      return false;
    x = x * 10 + (unsigned)(*s - '0');
  }
  *v = x;
  return true;
}
static bool utf8(const unsigned char *s, size_t n) {
  for (size_t i = 0; i < n;) {
    unsigned c = s[i++];
    if (c < 128)
      continue;
    unsigned k;
    uint32_t v, min;
    if (c >= 0xc2 && c <= 0xdf) {
      k = 1;
      v = c & 31;
      min = 128;
    } else if (c >= 0xe0 && c <= 0xef) {
      k = 2;
      v = c & 15;
      min = 2048;
    } else if (c >= 0xf0 && c <= 0xf4) {
      k = 3;
      v = c & 7;
      min = 65536;
    } else
      return false;
    if (i + k > n)
      return false;
    while (k--) {
      unsigned z = s[i++];
      if ((z & 0xc0) != 0x80)
        return false;
      v = (v << 6) | (z & 63);
    }
    if (v < min || v > 0x10ffff || (v >= 0xd800 && v <= 0xdfff))
      return false;
  }
  return true;
}
static int fixed_open(int d, const char *path, int extra) {
  struct open_how h = {.flags = O_RDONLY | O_CLOEXEC | (uint64_t)extra,
                       .resolve = RESOLVE_BENEATH | RESOLVE_NO_SYMLINKS |
                                  RESOLVE_NO_MAGICLINKS};
  return (int)syscall(SYS_openat2, d, path, &h, sizeof h);
}
/* One byte beyond the limit distinguishes exact-sized files from truncation. */
static unsigned char *read_fd(int fd, size_t limit, size_t *size) {
  unsigned char *b = malloc(limit + 1);
  if (!b)
    return NULL;
  size_t n = 0;
  while (n <= limit) {
    if (expired()) {
      errno = ETIMEDOUT;
      free(b);
      return NULL;
    }
    ssize_t k = read(fd, b + n, limit + 1 - n);
    if (k < 0) {
      if (errno == EINTR)
        continue;
      free(b);
      return NULL;
    }
    if (!k)
      break;
    n += (size_t)k;
  }
  if (n > limit) {
    errno = EFBIG;
    free(b);
    return NULL;
  }
  *size = n;
  return b;
}
static unsigned char *read_proc(const char *path, size_t limit, size_t *size) {
  int fd = fixed_open(ev.procfd, path, 0);
  if (fd < 0)
    return NULL;
  unsigned char *b = read_fd(fd, limit, size);
  int e = errno;
  close(fd);
  errno = e;
  return b;
}
static bool read_text(const char *path, char *out, size_t cap) {
  size_t n;
  unsigned char *b = read_proc(path, cap + 1, &n);
  if (!b)
    return false;
  while (n && (b[n - 1] == '\n' || b[n - 1] == '\r'))
    n--;
  if (n >= cap) {
    free(b);
    errno = EFBIG;
    return false;
  }
  memcpy(out, b, n);
  out[n] = 0;
  free(b);
  return true;
}
static bool parse_stat(const unsigned char *data, size_t n, struct process *p) {
  char *b = malloc(n + 1);
  if (!b)
    return false;
  memcpy(b, data, n);
  b[n] = 0;
  char *open = strchr(b, '('), *end = strrchr(b, ')');
  bool ok = false;
  uint64_t pid = 0;
  if (!open || !end || end <= open || open == b || open[-1] != ' ')
    goto done;
  open[-1] = 0;
  if (!uint_value(b, &pid) || pid != p->pid)
    goto done;
  char *save = NULL, *part = strtok_r(end + 1, " \n", &save);
  unsigned field = 3;
  uint64_t pp = 0, pg = 0, start = 0;
  while (part) {
    if (field == 4 && !uint_value(part, &pp))
      goto done;
    if (field == 5 && !uint_value(part, &pg))
      goto done;
    if (field == 22 && !uint_value(part, &start))
      goto done;
    field++;
    part = strtok_r(NULL, " \n", &save);
  }
  if (field < 23 || pp > UINT32_MAX || pg > UINT32_MAX || !start)
    goto done;
  p->ppid = (uint32_t)pp;
  p->pgid = (uint32_t)pg;
  p->start = start;
  ok = true;
done:
  free(b);
  return ok;
}
static bool read_stat(uint32_t pid, struct process *p) {
  char path[48];
  snprintf(path, sizeof path, "%u/stat", pid);
  size_t n;
  unsigned char *b = read_proc(path, 65536, &n);
  if (!b)
    return false;
  p->pid = pid;
  bool ok = parse_stat(b, n, p);
  free(b);
  if (!ok)
    errno = EINVAL;
  return ok;
}
static char *retain(struct scan *s, const char *v, size_t n) {
  if (n > MAX_FIELD || !utf8((const unsigned char *)v, n) ||
      s->retained + n + 1 > MAX_RETAINED) {
    errno = EFBIG;
    return NULL;
  }
  char *r = malloc(n + 1);
  if (!r)
    return NULL;
  memcpy(r, v, n);
  r[n] = 0;
  s->retained += n + 1;
  return r;
}
static bool parse_env(struct scan *s, struct process *p, unsigned char *b,
                      size_t n) {
  if (n && b[n - 1])
    return false;
  char *seen[4096];
  size_t seen_n = 0;
  char *fallback = NULL;
  bool ok = true;
  for (size_t at = 0; at < n;) {
    if (expired()) {
      ok = false;
      errno = ETIMEDOUT;
      break;
    }
    size_t len = strlen((char *)b + at);
    char *entry = (char *)b + at;
    at += len + 1;
    if (!len)
      continue;
    char *eq = memchr(entry, '=', len);
    if (!eq || eq == entry) {
      ok = false;
      break;
    }
    size_t key = (size_t)(eq - entry), value = len - key - 1;
    *eq = 0;
    if (strncmp(entry, "GC_", 3))
      continue;
    if (seen_n == 4096) {
      ok = false;
      errno = EFBIG;
      break;
    }
    for (size_t i = 0; i < seen_n; i++)
      if (!strcmp(seen[i], entry)) {
        ok = false;
        break;
      }
    if (!ok)
      break;
    seen[seen_n++] = entry;
    char **dst = NULL;
    if (!strcmp(entry, "GC_SESSION_ID"))
      dst = &p->sid;
    else if (!strcmp(entry, "GC_CITY_PATH"))
      dst = &p->city;
    else if (!strcmp(entry, "GC_CITY"))
      dst = &fallback;
    else if (!strcmp(entry, "GC_TEMPLATE"))
      dst = &p->template;
    else if (!strcmp(entry, "GC_INSTANCE_TOKEN")) {
      if (value)
        sha256_sum(eq + 1, value, p->token);
      continue;
    } else if (!strcmp(entry, "GC_RUNTIME_EPOCH")) {
      if (!uint_value(eq + 1, &p->epoch) || p->epoch > INT32_MAX) {
        ok = false;
        break;
      }
      continue;
    }
    if (dst && !(*dst = retain(s, eq + 1, value))) {
      ok = false;
      break;
    }
  }
  if ((!p->city || !*p->city) && fallback) {
    free(p->city);
    p->city = fallback;
    fallback = NULL;
  }
  free(fallback);
  return ok;
}
static bool infra(const char *name) {
  if (!name)
    return false;
  const char *base = strrchr(name, '/');
  base = base ? base + 1 : name;
  return !strcmp(base, "tmux") || !strcmp(base, "tmux:") ||
         !strcmp(base, "tmux: server") || !strcmp(base, "tmux: client");
}
static bool read_process(struct scan *s, struct process *p) {
  uint32_t pid = p->pid;
  struct process a = {0}, z = {0};
  if (!read_stat(pid, &a)) {
    add_error("process_unavailable", "stat", pid, errno);
    return false;
  }
  p->ppid = a.ppid;
  p->pgid = a.pgid;
  p->start = a.start;
  char path[48];
  snprintf(path, sizeof path, "%u/environ", pid);
  size_t n;
  unsigned char *b = read_proc(path, MAX_ENV, &n);
  if (!b) {
    add_error(errno == EFBIG ? "limit_reached" : "process_unavailable",
              "environ", pid, errno);
    return false;
  }
  errno = EINVAL;
  bool ok = parse_env(s, p, b, n);
  volatile unsigned char *wipe = b;
  for (size_t i = 0; i < n; i++)
    wipe[i] = 0;
  free(b);
  if (!ok) {
    add_error(errno == EFBIG       ? "limit_reached"
              : errno == ETIMEDOUT ? "deadline"
                                   : "malformed_environment",
              "environ", pid, errno);
    return false;
  }
  snprintf(path, sizeof path, "%u/comm", pid);
  b = read_proc(path, 256, &n);
  if (!b) {
    add_error("process_unavailable", "comm", pid, errno);
    return false;
  }
  while (n && (b[n - 1] == '\n' || b[n - 1] == '\r'))
    n--;
  p->name = retain(s, (char *)b, n);
  free(b);
  if (!p->name) {
    add_error("malformed_identity", "comm", pid, errno);
    return false;
  }
  if (!read_stat(pid, &z)) {
    add_error("process_unavailable", "stat", pid, errno);
    return false;
  }
  if (a.start != z.start || a.ppid != z.ppid || a.pgid != z.pgid) {
    add_error("incarnation_changed", "stat", pid, 0);
    return false;
  }
  p->valid = true;
  return true;
}
struct linux_dirent64 {
  uint64_t ino;
  int64_t off;
  unsigned short reclen;
  unsigned char type;
  char name[];
};
static int compare_process(const void *a, const void *b) {
  uint32_t x = ((const struct process *)a)->pid,
           y = ((const struct process *)b)->pid;
  return (x > y) - (x < y);
}
static struct process *parent(struct scan *s, uint32_t pid) {
  struct process key = {.pid = pid};
  return bsearch(&key, s->p, s->n, sizeof *s->p, compare_process);
}
static void digest_field(sha256_ctx *h, const char *s) {
  uint32_t n = htonl(s ? (uint32_t)strlen(s) : 0);
  sha256_update(h, &n, 4);
  if (s)
    sha256_update(h, s, strlen(s));
}
static void scan(struct scan *s) {
  s->p = calloc(MAX_PIDS, sizeof *s->p);
  if (!s->p) {
    add_error("allocation_failed", "enumerate", 0, ENOMEM);
    return;
  }
  int d = fixed_open(ev.procfd, ".", O_DIRECTORY);
  if (d < 0) {
    add_error("enumeration_failed", "enumerate", 0, errno);
    return;
  }
  unsigned char buf[32768];
  while (true) {
    if (expired()) {
      add_error("deadline", "enumerate", 0, ETIMEDOUT);
      break;
    }
    int n = (int)syscall(SYS_getdents64, d, buf, sizeof buf);
    if (n < 0) {
      add_error("enumeration_failed", "enumerate", 0, errno);
      break;
    }
    if (!n)
      break;
    for (int at = 0; at < n;) {
      struct linux_dirent64 *ent = (void *)(buf + at);
      if (ent->reclen < offsetof(struct linux_dirent64, name) + 1 ||
          at + ent->reclen > n ||
          !memchr(ent->name, 0,
                  ent->reclen - offsetof(struct linux_dirent64, name))) {
        add_error("malformed_directory", "enumerate", 0, 0);
        at = n;
        break;
      }
      uint64_t id;
      if (uint_value(ent->name, &id) && id > 0) {
        if (id > INT32_MAX) {
          add_error("malformed_identity", "enumerate", 0, 0);
        } else if (s->n == MAX_PIDS) {
          add_error("limit_reached", "enumerate", 0, EFBIG);
          at = n;
          break;
        } else
          s->p[s->n++].pid = (uint32_t)id;
      }
      at += ent->reclen;
    }
    if (s->n == MAX_PIDS) {
      add_error("limit_reached", "enumerate", 0, EFBIG);
      break;
    }
  }
  close(d);
  qsort(s->p, s->n, sizeof *s->p, compare_process);
  sha256_ctx hash;
  sha256_init(&hash);
  for (size_t i = 0; i < s->n; i++) {
    struct process *p = &s->p[i];
    if (i && p->pid == s->p[i - 1].pid) {
      add_error("duplicate_pid", "enumerate", p->pid, 0);
      continue;
    }
    if (expired()) {
      add_error("deadline", "stat", p->pid, ETIMEDOUT);
      break;
    }
    read_process(s, p);
    char identity[128];
    snprintf(identity, sizeof identity, "%u:%u:%u:%" PRIu64 ":%" PRIu64 ":%d",
             p->pid, p->ppid, p->pgid, p->start, p->epoch, p->valid);
    digest_field(&hash, identity);
    digest_field(&hash, p->name);
    digest_field(&hash, p->sid);
    digest_field(&hash, p->city);
    digest_field(&hash, p->template);
    digest_field(&hash, p->token);
  }
  sha256_hex(&hash, s->digest);
  for (size_t i = 0; i < s->n; i++) {
    struct process *p = &s->p[i];
    if (!p->valid || p->pid <= 1 || !p->sid || !*p->sid || infra(p->name))
      continue;
    struct process *par = parent(s, p->ppid);
    if (p->ppid > 1 && (!par || !par->valid)) {
      add_error("parent_unavailable", "stat", p->pid, 0);
      continue;
    }
    if (par && par->sid && !strcmp(par->sid, p->sid) && !infra(par->name))
      continue;
    if (!p->city || !*p->city || !p->template || !*p->template || !p->epoch ||
        !*p->token) {
      add_error("incomplete_identity", "environ", p->pid, 0);
      continue;
    }
    p->root = true;
    p->parent_infra = par && infra(par->name);
  }
}
static bool peer_binding(void) {
  struct ucred peer;
  socklen_t n = sizeof peer;
  if (getsockopt(CONNECTION_FD, SOL_SOCKET, SO_PEERCRED, &peer, &n) ||
      n != sizeof peer)
    return false;
  struct process p = {0};
  return peer.pid > 0 && (uint32_t)peer.pid == ev.binding.pid &&
         peer.uid == ev.binding.uid && read_stat((uint32_t)peer.pid, &p) &&
         p.start == ev.binding.start;
}
static bool parse_binding(unsigned char *buf, size_t n) {
  if (n == 0 || n > 4096 || memchr(buf, 0, n))
    return false;
  buf[n] = 0;
  unsigned seen = 0;
  char *save = NULL, *line = strtok_r((char *)buf, "\n", &save);
  while (line) {
    char *eq = strchr(line, '=');
    if (!eq || strchr(eq + 1, '='))
      return false;
    *eq++ = 0;
    unsigned bit;
    char *dst = NULL;
    size_t cap = 0;
    uint64_t v;
    if (!strcmp(line, "boot_id")) {
      if (!uuid_string(eq))
        return false;
      bit = 1;
      dst = ev.binding.boot;
      cap = sizeof ev.binding.boot;
    } else if (!strcmp(line, "controller_pid")) {
      bit = 2;
      if (!uint_value(eq, &v) || !v || v > INT32_MAX)
        return false;
      ev.binding.pid = (uint32_t)v;
    } else if (!strcmp(line, "controller_uid")) {
      bit = 4;
      if (!uint_value(eq, &v) || v > UINT32_MAX)
        return false;
      ev.binding.uid = (uint32_t)v;
    } else if (!strcmp(line, "controller_start_ticks")) {
      bit = 8;
      if (!uint_value(eq, &v) || !v)
        return false;
      ev.binding.start = v;
    } else if (!strcmp(line, "controller_source_revision")) {
      bit = 16;
      dst = ev.binding.source;
      cap = sizeof ev.binding.source;
      if (!hex_string(eq, 40))
        return false;
    } else if (!strcmp(line, "controller_binary_sha256")) {
      bit = 32;
      dst = ev.binding.binary;
      cap = sizeof ev.binding.binary;
      if (!hex_string(eq, 64))
        return false;
    } else if (!strcmp(line, "pid_namespace_identity")) {
      if (!namespace_string(eq))
        return false;
      bit = 64;
      dst = ev.binding.ns;
      cap = sizeof ev.binding.ns;
    } else if (!strcmp(line, "helper_binary_sha256")) {
      bit = 128;
      dst = ev.binding.helper_binary;
      cap = sizeof ev.binding.helper_binary;
      if (!hex_string(eq, 64))
        return false;
    } else
      return false;
    if (seen & bit)
      return false;
    seen |= bit;
    if (dst) {
      if (strlen(eq) >= cap || !*eq)
        return false;
      strcpy(dst, eq);
    }
    line = strtok_r(NULL, "\n", &save);
  }
  return seen == 255;
}
static bool root_owned(int fd) {
  struct stat st;
  return !fstat(fd, &st) && S_ISREG(st.st_mode) && st.st_uid == 0 &&
         !(st.st_mode & 0022);
}
static bool binding_parents_owned(void) {
  const char *paths[] = {"/", "/etc", "/etc/gascity-observer"};
  for (size_t i = 0; i < sizeof paths / sizeof paths[0]; i++) {
    int fd = open(paths[i], O_RDONLY | O_DIRECTORY | O_CLOEXEC | O_NOFOLLOW);
    if (fd < 0)
      return false;
    struct stat st;
    bool ok = !fstat(fd, &st) && st.st_uid == 0 && !(st.st_mode & 0022);
    close(fd);
    if (!ok)
      return false;
  }
  return true;
}
static bool load_binding(void) {
  int fd = open(binding_path, O_RDONLY | O_CLOEXEC | O_NOFOLLOW);
  if (fd < 0)
    return false;
  if (!fixture && (!binding_parents_owned() || !root_owned(fd))) {
    close(fd);
    return false;
  }
  size_t n;
  unsigned char *b = read_fd(fd, 4096, &n);
  close(fd);
  if (!b)
    return false;
  bool ok = parse_binding(b, n);
  free(b);
  return ok;
}
static bool self_hash(void) {
  int fd = open("/proc/self/exe", O_RDONLY | O_CLOEXEC);
  if (fd < 0)
    return false;
  if (!fixture && !root_owned(fd)) {
    close(fd);
    return false;
  }
  unsigned char b[32768];
  sha256_ctx h;
  sha256_init(&h);
  size_t total = 0;
  bool ok = true;
  for (;;) {
    if (expired()) {
      ok = false;
      break;
    }
    ssize_t n = read(fd, b, sizeof b);
    if (n < 0) {
      ok = false;
      break;
    }
    if (!n)
      break;
    total += (size_t)n;
    if (total > 32u * 1024u * 1024u) {
      ok = false;
      break;
    }
    sha256_update(&h, b, (size_t)n);
  }
  close(fd);
  if (ok)
    sha256_hex(&h, ev.binary);
  return ok;
}
static bool namespace_identity(
    char *out,
    size_t cap) { /* Only fixed startup namespace links; never caller input. */
  char path[256];
  if (fixture) {
    return read_text("fixture_namespace", out, cap);
  }
  snprintf(path, sizeof path, "%s/1/ns/pid", proc_path);
  ssize_t n = readlink(path, out, cap - 1);
  if (n < 0 || (size_t)n >= cap - 1)
    return false;
  out[n] = 0;
  char own[80];
  n = readlink("/proc/self/ns/pid", own, sizeof own - 1);
  if (n < 0 || (size_t)n >= sizeof own - 1)
    return false;
  own[n] = 0;
  return !strcmp(out, own);
}
static bool proc_mount_visible(void) {
  if (fixture)
    return true;
  struct statfs st;
  if (fstatfs(ev.procfd, &st) || st.f_type != PROC_SUPER_MAGIC)
    return false; /* Reject every proc mount with restrictive options, rather
                     than guessing the mount exposing /proc. */
  int fd = open("/proc/self/mountinfo", O_RDONLY | O_CLOEXEC | O_NOFOLLOW);
  if (fd < 0)
    return false;
  size_t n;
  unsigned char *b = read_fd(fd, 1024u * 1024u, &n);
  close(fd);
  if (!b)
    return false;
  b[n] = 0;
  bool found = false, ok = true;
  char *save = NULL, *line = strtok_r((char *)b, "\n", &save);
  while (line) {
    char *sep = strstr(line, " - proc ");
    if (sep) {
      found = true;
      if (strstr(line, "hidepid=") && !strstr(line, "hidepid=0"))
        ok = false;
      if (strstr(line, "subset=pid"))
        ok = false;
    }
    line = strtok_r(NULL, "\n", &save);
  }
  free(b);
  return found && ok;
}
/* Strict tiny JSON: only two unescaped string keys, no arrays/numbers/paths. */
static const char *space(const char *s) {
  while (*s == ' ' || *s == '\n' || *s == '\r' || *s == '\t')
    s++;
  return s;
}
static const char *json_string(const char *s, char *out, size_t cap) {
  s = space(s);
  if (*s++ != '"')
    return NULL;
  size_t n = 0;
  while (*s && *s != '"') {
    unsigned char c = (unsigned char)*s++;
    if (c < 32 || c == '\\' || n + 1 >= cap)
      return NULL;
    out[n++] = (char)c;
  }
  if (*s != '"')
    return NULL;
  out[n] = 0;
  return s + 1;
}
static bool parse_request(char *b) {
  const char *s = space(b);
  if (*s++ != '{')
    return false;
  unsigned fields = 0;
  for (;;) {
    char key[64], value[128];
    s = json_string(s, key, sizeof key);
    if (!s)
      return false;
    s = space(s);
    if (*s++ != ':')
      return false;
    s = json_string(s, value, sizeof value);
    if (!s)
      return false;
    if (!strcmp(key, "schema")) {
      if ((fields & 1) || strcmp(value, "observe-host-processes/v1"))
        return false;
      fields |= 1;
    } else if (!strcmp(key, "request_nonce")) {
      if ((fields & 2) || !hex_string(value, 64))
        return false;
      fields |= 2;
      strcpy(ev.nonce, value);
    } else
      return false;
    s = space(s);
    if (*s == '}') {
      s = space(s + 1);
      return !*s && fields == 3;
    }
    if (*s++ != ',')
      return false;
  }
}
static bool receive_request(void) {
  unsigned char buf[MAX_REQUEST + 5], control[CMSG_SPACE(sizeof(int) * 16)];
  size_t got = 0;
  for (;;) {
    if (expired())
      return false;
    struct pollfd p = {.fd = CONNECTION_FD, .events = POLLIN};
    int remaining =
        10000 -
        (int)(mono_ms() - ((uint64_t)ev.monotonic_start.tv_sec * 1000u +
                           (uint64_t)ev.monotonic_start.tv_nsec / 1000000u));
    if (poll(&p, 1, remaining) <= 0)
      return false;
    struct iovec iov = {.iov_base = buf + got, .iov_len = sizeof buf - got};
    struct msghdr msg = {.msg_iov = &iov,
                         .msg_iovlen = 1,
                         .msg_control = control,
                         .msg_controllen = sizeof control};
    ssize_t n = recvmsg(CONNECTION_FD, &msg, MSG_CMSG_CLOEXEC);
    if (n < 0)
      return false;
    bool ancillary =
        (msg.msg_flags & MSG_CTRUNC) || CMSG_FIRSTHDR(&msg) != NULL;
    for (struct cmsghdr *c = CMSG_FIRSTHDR(&msg); c; c = CMSG_NXTHDR(&msg, c))
      if (c->cmsg_level == SOL_SOCKET && c->cmsg_type == SCM_RIGHTS) {
        size_t z = (c->cmsg_len - CMSG_LEN(0)) / sizeof(int);
        int *fds = (int *)CMSG_DATA(c);
        for (size_t i = 0; i < z; i++)
          close(fds[i]);
      }
    if (ancillary)
      return false;
    if (!n)
      break;
    got += (size_t)n;
    if (got == sizeof buf)
      return false;
    if (got >= 4) {
      uint32_t len;
      memcpy(&len, buf, 4);
      len = ntohl(len);
      if (!len || len > MAX_REQUEST || got > (size_t)len + 4)
        return false;
    }
  }
  if (got < 4)
    return false;
  uint32_t len;
  memcpy(&len, buf, 4);
  len = ntohl(len);
  if (got != (size_t)len + 4 || memchr(buf + 4, 0, len))
    return false;
  buf[got] = 0;
  return parse_request((char *)buf + 4);
}
static int install_seccomp(void) {
#if defined(__x86_64__)
#define EXPECT_ARCH AUDIT_ARCH_X86_64
#else
  return -1;
#endif
#ifdef EXPECT_ARCH
#define ALLOW(nr)                                                              \
  BPF_JUMP(BPF_JMP | BPF_JEQ | BPF_K, (nr), 0, 1),                             \
      BPF_STMT(BPF_RET | BPF_K, SECCOMP_RET_ALLOW)
  struct sock_filter f[] = {
      BPF_STMT(BPF_LD | BPF_W | BPF_ABS, offsetof(struct seccomp_data, arch)),
      BPF_JUMP(BPF_JMP | BPF_JEQ | BPF_K, EXPECT_ARCH, 1, 0),
      BPF_STMT(BPF_RET | BPF_K, SECCOMP_RET_KILL_PROCESS),
      BPF_STMT(BPF_LD | BPF_W | BPF_ABS, offsetof(struct seccomp_data, nr)),
      /* write is the sole mutation syscall: response connection FD3 only. */
      BPF_JUMP(BPF_JMP | BPF_JEQ | BPF_K, SYS_write, 0, 4),
      BPF_STMT(BPF_LD | BPF_W | BPF_ABS,
               offsetof(struct seccomp_data, args[0])),
      BPF_JUMP(BPF_JMP | BPF_JEQ | BPF_K, CONNECTION_FD, 0, 1),
      BPF_STMT(BPF_RET | BPF_K, SECCOMP_RET_ALLOW),
      BPF_STMT(BPF_RET | BPF_K, SECCOMP_RET_ERRNO | EPERM),
      /* mmap/mprotect never grant executable memory. */
      BPF_JUMP(BPF_JMP | BPF_JEQ | BPF_K, SYS_mmap, 1, 0),
      BPF_JUMP(BPF_JMP | BPF_JEQ | BPF_K, SYS_mprotect, 0, 4),
      BPF_STMT(BPF_LD | BPF_W | BPF_ABS,
               offsetof(struct seccomp_data, args[2])),
      BPF_JUMP(BPF_JMP | BPF_JSET | BPF_K, 4, 0, 1),
      BPF_STMT(BPF_RET | BPF_K, SECCOMP_RET_ERRNO | EPERM),
      BPF_STMT(BPF_RET | BPF_K, SECCOMP_RET_ALLOW), ALLOW(SYS_read),
      ALLOW(SYS_close), ALLOW(SYS_openat2), ALLOW(SYS_getdents64),
      ALLOW(SYS_fstat), ALLOW(SYS_newfstatat), ALLOW(SYS_clock_gettime),
      ALLOW(SYS_brk), ALLOW(SYS_munmap), ALLOW(SYS_mremap),
      ALLOW(SYS_getsockopt), ALLOW(SYS_poll), ALLOW(SYS_exit),
      ALLOW(SYS_exit_group),
      BPF_STMT(BPF_RET | BPF_K, SECCOMP_RET_ERRNO | EPERM)};
  struct sock_fprog p = {.len = (unsigned short)(sizeof f / sizeof f[0]),
                         .filter = f};
  if (prctl(PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0))
    return -1;
  return (int)syscall(SYS_seccomp, SECCOMP_SET_MODE_FILTER, 0, &p);
#undef ALLOW
#endif
}
struct output {
  char *b;
  size_t n;
  bool overflow;
};
static void emit(struct output *o, const char *fmt, ...) {
  if (o->overflow)
    return;
  va_list args;
  va_start(args, fmt);
  int n = vsnprintf(o->b + o->n, MAX_OUTPUT - o->n, fmt, args);
  va_end(args);
  if (n < 0 || (size_t)n >= MAX_OUTPUT - o->n) {
    o->overflow = true;
    return;
  }
  o->n += (size_t)n;
}
static void string(struct output *o, const char *s) {
  emit(o, "\"");
  if (s)
    for (const unsigned char *p = (const unsigned char *)s; *p; p++) {
      if (*p == '"' || *p == '\\')
        emit(o, "\\%c", *p);
      else if (*p < 32)
        emit(o, "\\u%04x", *p);
      else
        emit(o, "%c", *p);
    }
  emit(o, "\"");
}
static void output(struct output *o, bool include_roots) {
  emit(o,
       "{\"schema\":\"host-process-evidence/"
       "v1\",\"scope\":\"%s\",\"request_nonce\":",
       fixture ? "fixture_procfs" : "host_procfs");
  string(o, ev.nonce);
  emit(o,
       ",\"helper_source_revision\":\"%s\",\"helper_binary_sha256\":\"%s\","
       "\"policy_digest\":\"%s\",\"boot_id\":",
       HELPER_SOURCE_REVISION, ev.binary, ev.policy);
  string(o, ev.boot);
  emit(o, ",\"pid_namespace_identity\":");
  string(o, ev.ns);
  emit(o,
       ",\"started_at\":\"%s\",\"finished_at\":\"%s\",\"duration_ms\":%" PRIu64
       ",\"complete\":%s,\"enumerated_count_before\":%zu,\"enumerated_count_"
       "after\":%zu,\"enumeration_digest_before\":\"%s\",\"enumeration_digest_"
       "after\":\"%s\",\"caller_binding\":{\"pid\":%u,\"uid\":%u,\"start_"
       "ticks\":\"%" PRIu64
       "\",\"boot_id\":\"%s\",\"controller_source_revision\":\"%s\","
       "\"controller_binary_sha256\":\"%s\"},\"roots\":[",
       ev.started, ev.finished, ev.duration, ev.complete ? "true" : "false",
       ev.before.n, ev.after.n, ev.before.digest, ev.after.digest,
       ev.binding.pid, ev.binding.uid, ev.binding.start, ev.binding.boot,
       ev.binding.source, ev.binding.binary);
  bool comma = false;
  if (include_roots)
    for (size_t i = 0; i < ev.after.n; i++) {
      struct process *p = &ev.after.p[i];
      if (!p->root)
        continue;
      if (comma)
        emit(o, ",");
      comma = true;
      emit(o,
           "{\"pid\":%u,\"ppid\":%u,\"pgid\":%u,\"start_ticks\":\"%" PRIu64
           "\",\"epoch\":%" PRIu64 ",\"session_id\":",
           p->pid, p->ppid, p->pgid, p->start, p->epoch);
      string(o, p->sid);
      emit(o, ",\"city\":");
      string(o, p->city);
      emit(o, ",\"template\":");
      string(o, p->template);
      emit(o, ",\"instance_token_sha256\":\"%s\",\"name\":", p->token);
      string(o, p->name);
      emit(o, ",\"parent_is_provider_infrastructure\":%s,\"parent_name\":",
           p->parent_infra ? "true" : "false");
      struct process *par = parent(&ev.after, p->ppid);
      string(o, par ? par->name : "");
      emit(o, "}");
    }
  emit(o, "],\"errors\":[");
  for (size_t i = 0; i < ev.errors_n; i++) {
    struct error_item *e = &ev.errors[i];
    if (i)
      emit(o, ",");
    emit(o, "{\"reason\":\"%s\",\"operation\":\"%s\",\"pid\":%u,\"errno\":%d}",
         e->reason, e->operation, e->pid, e->error);
  }
  emit(o, "],\"errors_total\":%zu,\"errors_truncated\":%s}", ev.errors_total,
       ev.errors_total > ev.errors_n ? "true" : "false");
}
static void timestamp(struct timespec t, char out[40]) {
  struct tm utc;
  gmtime_r(&t.tv_sec, &utc);
  char date[24];
  strftime(date, sizeof date, "%Y-%m-%dT%H:%M:%S", &utc);
  snprintf(out, 40, "%s.%09ldZ", date, t.tv_nsec);
}
static bool send_output(struct output *o) {
  uint32_t size = htonl((uint32_t)o->n);
  unsigned char *p = (unsigned char *)&size;
  size_t left = 4;
  for (int frame = 0; frame < 2; frame++) {
    while (left) {
      if (expired())
        return false;
      struct pollfd pollfd = {.fd = CONNECTION_FD, .events = POLLOUT};
      if (poll(&pollfd, 1, 100) <= 0)
        return false;
      ssize_t n = write(CONNECTION_FD, p, left);
      if (n <= 0)
        return false;
      p += (size_t)n;
      left -= (size_t)n;
    }
    p = (unsigned char *)o->b;
    left = o->n;
  }
  return true;
}
#ifdef GC_HELPER_TEST
/* Probe only in the disposable fixture build. Every attempted operation must
 * fail before reaching the kernel operation; no production flag exposes it. */
static int sandbox_selftest(void) {
  pid_t own = getpid();
  if (install_seccomp())
    return 3;
#define DENIED(nr, a, b, c, d, e, f)                                           \
  do {                                                                         \
    errno = 0;                                                                 \
    long r = syscall(nr, a, b, c, d, e, f);                                    \
    if (r != -1 || errno != EPERM)                                             \
      return 4;                                                                \
  } while (0)
  DENIED(SYS_ptrace, 0, 0, 0, 0, 0, 0);
  DENIED(SYS_process_vm_readv, -1, 0, 0, 0, 0, 0);
  DENIED(SYS_process_vm_writev, -1, 0, 0, 0, 0, 0);
  DENIED(SYS_kill, own, 0, 0, 0, 0, 0);
  DENIED(SYS_tgkill, own, own, 0, 0, 0, 0);
  DENIED(SYS_execve, "/nonexistent-gc-observer-fixture", 0, 0, 0, 0, 0);
  DENIED(SYS_execveat, -1, "", 0, 0, 0, 0);
  DENIED(SYS_clone, 0, 0, 0, 0, 0, 0);
  DENIED(SYS_clone3, 0, 0, 0, 0, 0, 0);
  DENIED(SYS_socket, AF_INET, SOCK_STREAM, 0, 0, 0, 0);
  DENIED(SYS_sendmsg, CONNECTION_FD, 0, 0, 0, 0, 0);
  DENIED(SYS_recvmsg, CONNECTION_FD, 0, 0, 0, 0, 0);
  DENIED(SYS_openat, AT_FDCWD, "/nonexistent-gc-observer-fixture", O_RDONLY, 0,
         0, 0);
  DENIED(SYS_open_by_handle_at, -1, 0, 0, 0, 0, 0);
  DENIED(SYS_ioctl, -1, 0, 0, 0, 0, 0);
  DENIED(SYS_io_uring_setup, 0, 0, 0, 0, 0, 0);
  DENIED(SYS_pidfd_send_signal, -1, 0, 0, 0, 0, 0);
  DENIED(SYS_write, STDOUT_FILENO, "x", 1, 0, 0, 0);
#undef DENIED
  return 0;
}
#endif
int main(int argc, char **argv) {
  ev.complete = true;
  clock_gettime(CLOCK_MONOTONIC, &ev.monotonic_start);
  clock_gettime(CLOCK_REALTIME, &ev.realtime_start);
  timestamp(ev.realtime_start, ev.started);
  sha256_sum(POLICY, strlen(POLICY), ev.policy);
#ifdef GC_HELPER_TEST
  if (argc == 2 && !strcmp(argv[1], "--sandbox-selftest"))
    return sandbox_selftest();
  if (argc == 3) {
    fixture = true;
    proc_path = argv[1];
    binding_path = argv[2];
  }
#else
  (void)argv;
#endif
  if ((!fixture && argc != 1) || !hex_string(HELPER_SOURCE_REVISION, 40))
    return 2;
  /* A disconnected response peer is transport failure, not signal death.
   * This changes only this process's startup disposition; signal-send and
   * disposition-changing syscalls remain denied after seccomp installation. */
  struct sigaction ignore = {.sa_handler = SIG_IGN};
  if (sigemptyset(&ignore.sa_mask) || sigaction(SIGPIPE, &ignore, NULL))
    return 2;
  /* Fail closed before reading any client payload when startup policy/binding
   * fails. */
  if (!load_binding() || !self_hash() ||
      strcmp(ev.binary, ev.binding.helper_binary))
    return 2;
  ev.procfd = open(proc_path, O_RDONLY | O_DIRECTORY | O_CLOEXEC | O_NOFOLLOW);
  if (ev.procfd < 0)
    return 2;
  if (!read_text("sys/kernel/random/boot_id", ev.boot, sizeof ev.boot) ||
      strcmp(ev.boot, ev.binding.boot) ||
      !namespace_identity(ev.ns, sizeof ev.ns) ||
      strcmp(ev.ns, ev.binding.ns) || !proc_mount_visible() || !peer_binding())
    return 2;
  if (!receive_request())
    return 2;
  if (prctl(PR_SET_DUMPABLE, 0, 0, 0, 0))
    return 2;
  /* Nonblocking writes preserve the deadline; SIGPIPE is ignored above. */
  int flags = fcntl(CONNECTION_FD, F_GETFL);
  if (flags < 0 || fcntl(CONNECTION_FD, F_SETFL, flags | O_NONBLOCK))
    return 2;
  if (install_seccomp()) {
    add_error("sandbox_unavailable", "startup", 0, errno);
  } else {
    scan(&ev.before);
    scan(&ev.after);
    if (ev.before.n != ev.after.n || strcmp(ev.before.digest, ev.after.digest))
      add_error("coverage_changed", "enumerate", 0, 0);
    if (!peer_binding())
      add_error("caller_binding_changed", "binding", ev.binding.pid, 0);
    char boot[37];
    if (!read_text("sys/kernel/random/boot_id", boot, sizeof boot) ||
        strcmp(boot, ev.boot))
      add_error("boot_changed", "identity", 0, 0);
  }
  struct timespec finished;
  clock_gettime(CLOCK_REALTIME, &finished);
  timestamp(finished, ev.finished);
  ev.duration = mono_ms() - ((uint64_t)ev.monotonic_start.tv_sec * 1000u +
                             (uint64_t)ev.monotonic_start.tv_nsec / 1000000u);
  if (ev.duration >= 10000)
    add_error("deadline", "observation", 0, ETIMEDOUT);
  if (finished.tv_sec < ev.realtime_start.tv_sec ||
      (finished.tv_sec == ev.realtime_start.tv_sec &&
       finished.tv_nsec < ev.realtime_start.tv_nsec))
    add_error("clock_reversal", "identity", 0, 0);
  struct output o = {.b = malloc(MAX_OUTPUT)};
  if (!o.b)
    return 2;
  output(&o, true);
  if (o.overflow) {
    add_error("response_limit", "response", 0, EFBIG);
    o.n = 0;
    o.overflow = false;
    output(&o, false);
  }
  if (o.overflow || !send_output(&o))
    return 2;
  return ev.complete ? 0 : 1;
}
