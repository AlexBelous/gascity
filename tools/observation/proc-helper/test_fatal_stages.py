"""Owned stubbed actual-main execution; no production helper, proc, socket or RPC."""
import hashlib
import json
from pathlib import Path
import re
import subprocess
import tempfile
import unittest

HERE = Path(__file__).resolve().parent
BASE = HERE / "testdata/observer-v3-baseline666.c"
PROPOSED = HERE / "observer-v3.c"

# The entire real main body is compiled unchanged against finite owned stubs.
# Both versions see the same injected failure. No production include is loaded.
STUBS = r'''
#include <stdio.h>
#include <stdlib.h>
#include <stdbool.h>
#include <stdint.h>
#include <string.h>
#include <time.h>
#include <signal.h>
#include <errno.h>
#include <fcntl.h>
#define HELPER_SOURCE_REVISION "66677222b3fc1a623d1212e65ecb9683e52ec85a"
#define CENSUS_V3_POLICY "owned-stub-policy"
#define CONNECTION_FD 3
#define PR_SET_DUMPABLE 4
static const char *failure;
static char trace[2048];
static int sent=-1;
static bool gate(const char *s) {
  strcat(trace,s);strcat(trace,";");return strcmp(failure,s)!=0;
}
struct census_budget { int unused; };
struct census_owned_identity { int unused; };
struct census_round_journal { int unused; };
struct census_bootstrap {
  struct census_budget budget;
  struct census_owned_identity caller;
  struct census_round_journal journal;
};
static struct census_bootstrap census_v3;
static struct {
  bool complete,trusted_kernel;
  int processfd,active_pidfd,procfd;
  struct timespec monotonic_start,realtime_start;
  char started[64],policy[65],boot[37],ns[64];
  struct {char boot[37],ns[64];uint32_t pid;uint64_t start;} binding;
} ev;
struct census_capture_fault {const char *operation;uint32_t pid;uint64_t start;int error;};
struct history {unsigned n;};
struct census_global_source {struct history *history;};
struct census_resolution_ledger {int unused;};
struct json_buffer {char *data;size_t n;};
struct census_envelope {struct json_buffer *json;bool complete;};
struct output {char *b;size_t n;};
enum census_round_result {CENSUS_ROUNDS_INCOMPLETE,CENSUS_ROUNDS_SELECTED_PENDING_PROOF};
static bool hex_string(const char *s,int n) {(void)s;(void)n;return gate("source");}
static int owned_clock(int id,struct timespec *t) {
  (void)t;return gate(id==CLOCK_MONOTONIC?"clock_monotonic":"clock_realtime")?0:-1;
}
#define clock_gettime owned_clock
static void timestamp(struct timespec t,char *s) {(void)t;(void)s;(void)gate("timestamp");}
static void sha256_sum(const char *s,size_t n,char *o) {(void)s;(void)n;(void)o;(void)gate("policy_hash");}
static int owned_sigemptyset(sigset_t *s) {(void)s;return gate("sigemptyset")?0:-1;}
#undef sigemptyset
#define sigemptyset owned_sigemptyset
static int owned_sigaction(int n,const struct sigaction *a,struct sigaction *b) {
  (void)n;(void)a;(void)b;return gate("sigaction")?0:-1;
}
#define sigaction(n,a,b) owned_sigaction(n,a,b)
static bool census_bootstrap_init(struct census_bootstrap *b) {(void)b;return gate("bootstrap");}
static bool census_binding_v3(struct census_budget *b) {(void)b;return gate("binding");}
static bool census_self_hash_v3(struct census_budget *b) {(void)b;return gate("self_hash");}
static bool census_text_v3(struct census_budget *b,const char *p,char *o,size_t n) {
  (void)b;(void)p;(void)n;
  bool initial=o==ev.boot;
  bool ok=gate(initial?"boot_initial":"boot_final");
  strcpy(o,strcmp(failure,initial?"boot_binding":"boot_final_binding")==0?"wrong":"boot");
  return ok;
}
static bool namespace_identity(char *o,size_t n) {
  (void)n;bool ok=gate("namespace");strcpy(o,strcmp(failure,"namespace_binding")==0?"wrong":"namespace");return ok;
}
static bool census_mount_v3(struct census_budget *b) {(void)b;return gate("mount");}
static bool census_peer_v3(struct census_bootstrap *b,bool initial) {(void)b;return gate(initial?"caller_initial":"caller_final");}
static bool census_receive_request_v3(struct census_budget *b) {(void)b;return gate("request");}
static int prctl(int a,int b,int c,int d,int e) {(void)a;(void)b;(void)c;(void)d;(void)e;return gate("dumpable")?0:-1;}
static bool census_kernel_v3(struct census_budget *b) {(void)b;return gate("kernel");}
static int owned_fcntl(int fd,int op,...) {(void)fd;return gate(op==F_GETFL?"fd_get":"fd_nonblock")?0:-1;}
#define fcntl owned_fcntl
static int install_seccomp(void) {return gate("seccomp")?0:-1;}
#define census_fixed_enumeration 0
#define census_fixed_capture 0
static struct census_global_source *census_global_new(struct census_budget *b,void *x,int a,int c) {
  (void)b;(void)x;(void)a;(void)c;static struct history h={1};static struct census_global_source s={&h};return gate("global_alloc")?&s:NULL;
}
static struct census_resolution_ledger *census_resolution_new(struct census_budget *b) {
  (void)b;static struct census_resolution_ledger l;return gate("ledger_alloc")?&l:NULL;
}
static enum census_round_result census_choose_proven_continuation(struct census_global_source *s,struct census_resolution_ledger *p,struct census_round_journal *j) {
  (void)s;(void)p;(void)j;return gate("collection_incomplete")?CENSUS_ROUNDS_SELECTED_PENDING_PROOF:CENSUS_ROUNDS_INCOMPLETE;
}
static void census_global_error(struct census_global_source *s,unsigned n,const char *why,struct census_capture_fault f) {
  (void)s;(void)n;(void)f;(void)gate(why);
}
static struct census_envelope *census_envelope_new(struct census_global_source *s,struct census_resolution_ledger *p,struct census_round_journal *j) {
  (void)s;(void)p;(void)j;static struct json_buffer b={"owned-sentinel",14};static struct census_envelope e;
  e.json=&b;e.complete=ev.complete;return gate("envelope_alloc")?&e:NULL;
}
static bool census_envelope_finalize(struct census_envelope *e) {(void)e;return gate("envelope_finalize");}
static bool send_output(struct output *w) {
  if(!gate("send")) return false;
  if(w->n!=14 || strcmp(w->b,"owned-sentinel")) abort();sent=ev.complete;return true;
}
static void census_envelope_free(struct census_budget *b,struct census_envelope *e) {(void)b;(void)e;(void)gate("free_envelope");}
static void census_resolution_free(struct census_budget *b,struct census_resolution_ledger *l) {(void)b;(void)l;(void)gate("free_ledger");}
static void census_global_free(struct census_global_source *s) {(void)s;(void)gate("free_global");}
static void census_identity_clear(struct census_budget *b,struct census_owned_identity *i) {(void)b;(void)i;(void)gate("clear_identity");}
static void census_fd_close(struct census_budget *b,int fd) {(void)b;(void)fd;(void)gate("close_proc");}
'''

DRIVER = r'''
int main(int argc,char **argv) {
  if(argc!=2) return 100;
  failure=argv[1];strcpy(ev.binding.boot,"boot");strcpy(ev.binding.ns,"namespace");
  ev.binding.pid=42;ev.binding.start=123;
  int result=helper_main(strcmp(failure,"argc")==0?2:1,NULL);
  printf("{\"status\":%d,\"trace\":\"%s\",\"sent\":%d}\n",result,trace,sent);
  return 0;
}
'''


def main_text(source):
    return source[source.index("int main(int argc,char **argv) {"):].replace(
        "int main(int argc,char **argv) {", "static int helper_main(int argc,char **argv) {", 1)


class ActualMainTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.tmp = tempfile.TemporaryDirectory(prefix="gc-owned-fatal-stage-")
        cls.binaries = {}
        for name, path in (("baseline", BASE), ("proposed", PROPOSED)):
            source = path.read_text()
            enum = ""
            if name == "proposed":
                enum = re.search(r"enum census_v3_fatal_status \{.*?\n\};", source, re.S).group()
            cfile = Path(cls.tmp.name) / (name + ".owned-main-test.c")
            binary = Path(cls.tmp.name) / (name + ".owned-main-test")
            cfile.write_text(STUBS + enum + main_text(source) + DRIVER)
            built = subprocess.run(["cc", "-std=c11", "-D_POSIX_C_SOURCE=200809L", "-Wall", "-Wextra", "-Werror", str(cfile), "-o", str(binary)], capture_output=True, timeout=10)
            if built.returncode:
                raise RuntimeError(built.stderr.decode())
            cls.binaries[name] = binary

    @classmethod
    def tearDownClass(cls):
        cls.tmp.cleanup()

    def execute(self, version, fault):
        p = subprocess.run([str(self.binaries[version]), fault], check=True, capture_output=True, timeout=10)
        self.assertEqual(p.stderr, b"")
        return json.loads(p.stdout)

    def compare(self, fault, expected):
        before = self.execute("baseline", fault)
        after = self.execute("proposed", fault)
        self.assertEqual(before["trace"], after["trace"], fault)
        self.assertEqual(before["sent"], after["sent"], fault)
        self.assertEqual(after["status"], expected, fault)
        return before, after

    def test_all_fatal_gates_keep_exact_order_and_result(self):
        cases = {
            64: ["argc", "source"], 65: ["clock_monotonic", "clock_realtime"],
            66: ["sigemptyset", "sigaction"], 67: ["bootstrap"],
            68: ["binding"], 69: ["self_hash"], 70: ["boot_initial", "boot_binding"],
            71: ["namespace", "namespace_binding"], 72: ["mount"],
            73: ["caller_initial"], 74: ["request"], 75: ["dumpable"],
            76: ["kernel"], 77: ["fd_get"], 78: ["fd_nonblock"],
            79: ["seccomp"], 80: ["global_alloc"], 81: ["ledger_alloc"],
            82: ["envelope_alloc"], 83: ["envelope_finalize"], 84: ["send"],
        }
        for status, faults in cases.items():
            for fault in faults:
                with self.subTest(fault=fault):
                    before, after = self.compare(fault, status)
                    self.assertEqual(before["status"], 2)
                    self.assertEqual(after["sent"], -1)

    def test_success_keeps_zero_and_identical_cleanup(self):
        before, after = self.compare("none", 0)
        self.assertEqual(before["status"], 0)
        self.assertEqual(after["sent"], 1)
        self.assertTrue(after["trace"].endswith("clear_identity;close_proc;"))

    def test_typed_incomplete_and_final_fences_stay_status_one(self):
        for fault in ("collection_incomplete", "caller_final", "boot_final", "boot_final_binding"):
            with self.subTest(fault=fault):
                before, after = self.compare(fault, 1)
                self.assertEqual(before["status"], 1)
                self.assertEqual(after["sent"], 0)

    def test_closed_unique_non_authoritative_codes(self):
        codes = json.loads((HERE / "V3-FATAL-STAGE-CODES.json").read_text())
        self.assertEqual(set(map(int, codes)), set(range(64, 85)))
        self.assertEqual(len(set(codes.values())), 21)
        declared = {code: name.lower() for name, code in re.findall(
            r"CENSUS_V3_FATAL_([A-Z_]+) = ([0-9]+)", PROPOSED.read_text())}
        self.assertEqual(codes, declared)
        self.assertFalse({0, 1, 2}.intersection(map(int, codes)))

    def test_no_production_helper_or_legacy_source_edit(self):
        source = PROPOSED.read_text()
        self.assertEqual(source.count("#define GC_HELPER_V3_SEND_BUDGET 1\n"), 1)
        source = source.replace("#define GC_HELPER_V3_SEND_BUDGET 1\n", "", 1)
        source = source.replace('  /* Readiness and EINTR/EAGAIN continue only the same unsent suffix within\n   * the original wall budget. No re-encoding or new request deadline; limits\n   * and permanent write failures still cannot manufacture COMPLETE. */', '  /* No re-encoding/retry on any limit or write failure; original UNKNOWN is\n   * preserved, and transport failure cannot manufacture COMPLETE. */', 1)
        original = BASE.read_text()
        self.assertEqual(hashlib.sha256(BASE.read_bytes()).hexdigest(),
                         "3e76c07af2b2e4e6c1abd0efe4d86bc69f42ced54635bd2159bd05a40b48ea30")
        self.assertEqual(source[:source.index("/* V3 fatal-stage diagnostic ABI")],
                         original[:original.index("static struct census_bootstrap census_v3;")])
        self.assertNotRegex(main_text(source), r"fprintf|perror|syslog|stderr|STDERR|exit\(")
        self.assertEqual(source[source.index("  struct output wire="):].replace(
            "return CENSUS_V3_FATAL_SEND;", "return 2;"),
            original[original.index("  struct output wire="):])


if __name__ == "__main__":
    unittest.main(verbosity=2)
