"""Execute the actual C resolver against owned finite witness-boundary stubs.

This owns selector/call-order/custody negatives, not Linux kernel truth. The
separate owned-child kernel fixture must establish real pidfd/stat/UID evidence.
"""
from pathlib import Path
import subprocess
import tempfile
import unittest

HERE = Path(__file__).resolve().parent


def function(text, name):
    start = text.index('static inline bool ' + name + '(')
    opened = text.index('{', start)
    depth = 1
    for end in range(opened + 1, len(text)):
        depth += (text[end] == '{') - (text[end] == '}')
        if not depth:
            return text[start:end + 1]
    raise ValueError(name)


STUBS = r'''
#include <stdbool.h>
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <errno.h>
enum {CENSUS_EXIT_POLL_ZERO=1,CENSUS_EXIT_POLL_VALID_POLLIN=2};
enum {CENSUS_TERMINAL_ZOMBIE=1,CENSUS_RESOLUTION_KNOWN_TERMINAL=2,CENSUS_RESOLUTION_BOUND_BIRTH=3};
struct census_capture_fault {
 uint32_t pid;uint64_t start;const char *operation;int error;
 unsigned retained_stage,retained_pidfd_plus_one,retained_procdir_plus_one;
 uint64_t retained_start;uint32_t retained_uids[4];
 bool capture_diagnostic_available,diagnostic_bound_live,diagnostic_first_identity_present,bound_exit_valid;
 unsigned fields_pass,diagnostic_exit_poll;
};
struct census_global_fault {unsigned scan_index;const char *reason;struct census_capture_fault raw;};
struct census_history_row {struct census_capture_fault raw_fault;};
struct census_owned_identity {uint64_t start;uint32_t uids[4];};
struct census_budget {bool fd_failed;};
struct hist {unsigned n;struct census_history_row row;struct {int unused;} scans[8];};
struct census_global_source {unsigned errors_n;struct census_global_fault errors[3];struct hist *history;struct census_budget *budget;};
struct proof_source {int kind;const char *method;uint32_t pid;uint64_t start;int scan_index;uint64_t offset_ms;};
struct witness {unsigned stage,initial_poll;uint64_t bound_start;bool full_identity_revalidated;uint32_t uids_before[4],uids_after[4];};
struct census_typed_proof {struct proof_source source;struct witness exit_witness;};
struct census_typed_resolution {unsigned error_index,proof_index,classified_scan;int kind;};
struct census_resolution_ledger {bool denied;unsigned proofs_n,resolutions_n;struct census_typed_proof proofs[128];struct census_typed_resolution resolutions[128];};
static struct {struct {long tv_sec,tv_nsec;} monotonic_start;} ev;
static struct census_owned_identity prior={.start=123,.uids={1000,1000,1000,1000}};
static bool scope_ok=true,prior_ok=true,original_ok=true,companion_ok=true;
static unsigned original_calls,companion_calls;
static uint64_t mono_ms(void) {return 100;}
static bool census_exit_original_scope(const struct census_global_source *s) {(void)s;return scope_ok;}
static bool census_exit_full_prior(const struct census_global_source *s,uint32_t pid,unsigned through,const struct census_owned_identity **out,unsigned *scan) {
 (void)s;(void)pid;(void)through;*out=&prior;*scan=4;return prior_ok;
}
static struct census_history_row *census_history_find(void *scan,uint32_t pid) {(void)scan;(void)pid;extern struct hist history;return &history.row;}
static bool census_exit_original_full_zombie(struct census_budget *b,int fd,int dir,const struct census_owned_identity *p,uint32_t observed[4]) {
 (void)b;if(p->start!=123)return false;
 if(fd==10 && dir==11) {original_calls++;if(!original_ok)return false;}
 else if(fd==20 && dir==21) {companion_calls++;if(!companion_ok)return false;}
 else return false;
 memcpy(observed,p->uids,sizeof p->uids);return true;
}
static bool census_exit_original_zombie(struct census_budget *b,int fd,int dir,uint32_t pid,uint64_t start,const uint32_t uids[4],uint32_t observed[4]) {
 (void)pid;(void)start;(void)uids;return census_exit_original_full_zombie(b,fd,dir,&prior,observed);
}
static bool census_typed_proofs_valid(struct census_resolution_ledger *r) {(void)r;return true;}
static bool census_exit_history_valid(struct census_global_source *s,struct census_resolution_ledger *r) {(void)s;(void)r;return true;}
static void census_capture_custody_close(struct census_budget *b,struct census_capture_fault *f) {(void)b;f->retained_pidfd_plus_one=f->retained_procdir_plus_one=0;}
struct hist history;
'''

DRIVER = r'''
int main(int argc,char **argv) {
 if(argc!=2)return 100;
 const char *mode=argv[1];struct census_budget budget={0};history.n=7;
 struct census_global_source s={.errors_n=2,.history=&history,.budget=&budget};
 struct census_resolution_ledger ledger={0};
 s.errors[0]=(struct census_global_fault){.scan_index=4,.reason="uninspected_birth",.raw={.pid=777,.start=123,.operation="census"}};
 history.row.raw_fault=(struct census_capture_fault){.retained_stage=3,.retained_start=123,.retained_pidfd_plus_one=11,.retained_procdir_plus_one=12,.retained_uids={1000,1000,1000,1000}};
 s.errors[1]=(struct census_global_fault){.scan_index=7,.reason="process_unavailable",.raw={.pid=777,.operation="pidfd_poll",.error=ESTALE,.retained_stage=2,.retained_start=123,.retained_pidfd_plus_one=21,.retained_procdir_plus_one=22,.retained_uids={1000,1000,1000,1000}}};
 struct census_capture_fault *f=&s.errors[1].raw;
 bool positive=!strcmp(mode,"poll-positive") || !strcmp(mode,"env-positive");
 if(!strncmp(mode,"env-",4)) {
  f->operation="environ";f->error=ESRCH;f->start=123;f->retained_stage=1;
  f->capture_diagnostic_available=f->diagnostic_bound_live=true;f->fields_pass=1;f->diagnostic_exit_poll=CENSUS_EXIT_POLL_ZERO;
 }
 if(!strcmp(mode,"no-original")) history.row.raw_fault.retained_stage=0;
 else if(!strcmp(mode,"original-zero") || !strcmp(mode,"original-hup") || !strcmp(mode,"original-error") || !strcmp(mode,"original-reused")) original_ok=false;
 else if(!strcmp(mode,"no-companion-custody")) f->retained_stage=0;
 else if(!strcmp(mode,"wrong-stage")) f->retained_stage=1;
 else if(!strcmp(mode,"missing-pidfd")) f->retained_pidfd_plus_one=0;
 else if(!strcmp(mode,"missing-procdir")) f->retained_procdir_plus_one=0;
 else if(!strcmp(mode,"wrong-fd")) f->retained_pidfd_plus_one=31;
 else if(!strcmp(mode,"wrong-dir")) f->retained_procdir_plus_one=32;
 else if(!strcmp(mode,"wrong-start")) f->retained_start++;
 else if(!strcmp(mode,"wrong-uid")) f->retained_uids[2]++;
 else if(!strcmp(mode,"poll-zero") || !strcmp(mode,"poll-hup") || !strcmp(mode,"poll-error") || !strcmp(mode,"pid-reused")) companion_ok=false;
 else if(!strcmp(mode,"namespace") || !strcmp(mode,"deadline") || !strcmp(mode,"scope-refused") || !strcmp(mode,"resource")) scope_ok=false;
 else if(!strcmp(mode,"managed") || !strcmp(mode,"root") || !strcmp(mode,"prior-changed")) prior_ok=false;
 else if(!strcmp(mode,"raw-start")) f->start=123;
 else if(!strcmp(mode,"raw-errno")) f->error=ESRCH;
 else if(!strcmp(mode,"raw-diagnostic")) f->capture_diagnostic_available=true;
 else if(!strcmp(mode,"env-second-pass")) f->fields_pass=2;
 else if(!strcmp(mode,"duplicate-poll") || !strcmp(mode,"mixed-two-companions")) {
  s.errors_n=3;s.errors[2]=s.errors[1];
  if(!strcmp(mode,"mixed-two-companions")) {
   s.errors[2].raw.operation="environ";s.errors[2].raw.error=ESRCH;s.errors[2].raw.start=123;
  }
 }
 bool ret=census_resolve_exit_windows(&s,&ledger);
 bool accepted=ret && ledger.proofs_n==1 && ledger.resolutions_n==2 && ledger.proofs[0].exit_witness.stage==3;
 if(accepted && (original_calls!=1 || companion_calls!=1 || ledger.proofs[0].exit_witness.initial_poll!=CENSUS_EXIT_POLL_ZERO)) return 3;
 if(accepted!=positive) {fprintf(stderr,"mode=%s accepted=%d expected=%d original=%u companion=%u\n",mode,accepted,positive,original_calls,companion_calls);return 1;}
 return 0;
}
'''


class OriginalFullCompanionProducerTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        text = (HERE / 'census-exit-retirement.h').read_text()
        parts = [function(text, 'census_birth_companion_diagnostic')]
        if 'static inline bool census_birth_poll_companion(' in text:
            parts.append(function(text, 'census_birth_poll_companion'))
        parts.append(function(text, 'census_resolve_exit_windows'))
        cls.tmp = tempfile.TemporaryDirectory(prefix='owned-original-full-poll-')
        source = Path(cls.tmp.name) / 'actual-resolver.c'
        source.write_text(STUBS + '\n'.join(parts) + DRIVER)
        cls.binary = source.with_suffix('')
        subprocess.run(['cc', '-std=c11', '-Wall', '-Wextra', '-Werror', str(source), '-o', str(cls.binary)], check=True)

    @classmethod
    def tearDownClass(cls):
        cls.tmp.cleanup()

    def test_genuine_both_pairs_are_required(self):
        for case in ['poll-positive', 'env-positive', 'no-original', 'original-zero', 'original-hup',
                     'original-error', 'original-reused', 'no-companion-custody', 'wrong-stage',
                     'missing-pidfd', 'missing-procdir', 'wrong-fd', 'wrong-dir', 'wrong-start',
                     'wrong-uid', 'poll-zero', 'poll-hup', 'poll-error', 'pid-reused', 'namespace',
                     'deadline', 'scope-refused', 'resource', 'managed', 'root', 'prior-changed',
                     'raw-start', 'raw-errno', 'raw-diagnostic', 'env-second-pass',
                     'duplicate-poll', 'mixed-two-companions']:
            with self.subTest(case=case):
                subprocess.run([str(self.binary), case], check=True)


if __name__ == '__main__':
    unittest.main()
