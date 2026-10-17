"""Owned portable transport proof: production C body, no helper/proc observer calls."""
import os
from pathlib import Path
import subprocess
import tempfile
import unittest

HERE = Path(__file__).resolve().parent


def production_send_body():
    source = (HERE / 'observer.c').read_text()
    start = source.index('static bool send_output(struct output *o) {')
    end = source.index('\n#ifdef GC_HELPER_TEST', start)
    return source[start:end]


PRELUDE = r'''
#include <arpa/inet.h>
#include <stdbool.h>
#include <stdint.h>
#include <stdlib.h>
#include <stdio.h>
#include <string.h>
#include <errno.h>
#include <unistd.h>
#include <fcntl.h>
#include <poll.h>
#include <signal.h>
#include <time.h>
#include <sys/socket.h>
#include <sys/wait.h>
static int connection_fd, mode, poll_calls, write_calls;
static uint64_t clock_offset_ms;
static size_t written_bytes;
#define CONNECTION_FD connection_fd
static struct { struct timespec monotonic_start; } ev;
struct output {char *b;size_t n;};
static uint64_t mono_ms(void) {struct timespec t;if(clock_gettime(CLOCK_MONOTONIC,&t))return UINT64_MAX;return (uint64_t)t.tv_sec*1000u+(uint64_t)t.tv_nsec/1000000u+clock_offset_ms;}
static bool expired(void) {uint64_t start=(uint64_t)ev.monotonic_start.tv_sec*1000u+(uint64_t)ev.monotonic_start.tv_nsec/1000000u;return mono_ms()-start>=10000u;}
static int test_poll(struct pollfd *fds,nfds_t n,int timeout) {poll_calls++;if(mode==2 && poll_calls==1){errno=EINTR;return -1;}int result=poll(fds,n,timeout);if(mode==7)clock_offset_ms=10000;return result;}
static ssize_t test_write(int fd,const void *p,size_t n) {write_calls++;if(write_calls==1 && (mode==3 || mode==4)){errno=mode==3?EINTR:EAGAIN;return -1;}ssize_t result=write(fd,p,n);if(result>0)written_bytes+=(size_t)result;if(mode==8 && written_bytes==262148)clock_offset_ms=10000;return result;}
#define poll test_poll
#define write test_write
'''
SUFFIX = r'''
#undef poll
#undef write
static void delay_ms(unsigned ms) {struct timespec t={(time_t)(ms/1000),(long)(ms%1000)*1000000};while(nanosleep(&t,&t)<0 && errno==EINTR){}}
int main(int argc,char **argv) {
 if(argc!=2)return 90;mode=atoi(argv[1]);signal(SIGPIPE,SIG_IGN);
 int fd[2];if(socketpair(AF_UNIX,SOCK_STREAM,0,fd))return 91;
 int size=4096;if(setsockopt(fd[0],SOL_SOCKET,SO_SNDBUF,&size,sizeof size))return 92;
 connection_fd=fd[0];if(fcntl(fd[0],F_SETFL,fcntl(fd[0],F_GETFL)|O_NONBLOCK))return 93;
 struct output out={calloc(1,262144),262144};if(!out.b)return 94;
 pid_t reader=-1;int start_pipe[2]={-1,-1};
 if(mode==1 || mode==2 || mode==3 || mode==4 || mode==8 || mode==9){
  if(pipe(start_pipe))return 95;
  reader=fork();if(reader<0)return 95;
  if(reader==0){close(fd[0]);close(start_pipe[1]);char token;ssize_t ready;do{ready=read(start_pipe[0],&token,1);}while(ready<0 && errno==EINTR);close(start_pipe[0]);if(ready!=1)_exit(96);if(mode==1 || mode==9)delay_ms(250);char b[8192];size_t got=0;ssize_t n;while((n=read(fd[1],b,sizeof b))>0)got+=(size_t)n;close(fd[1]);_exit(got==262148?0:1);}
  close(fd[1]);close(start_pipe[0]);
 }else if(mode==6)close(fd[1]);
 if(mode==9)delay_ms(300);
 clock_gettime(CLOCK_MONOTONIC,&ev.monotonic_start);
 if(mode==5){ev.monotonic_start.tv_sec-=10;ev.monotonic_start.tv_nsec+=200000000;if(ev.monotonic_start.tv_nsec>=1000000000){ev.monotonic_start.tv_sec++;ev.monotonic_start.tv_nsec-=1000000000;}}
 uint64_t began=mono_ms();
 /* Reader delay begins only after the parent has recorded the original budget. */
 if(reader>0){ssize_t ready;do{ready=write(start_pipe[1],"s",1);}while(ready<0 && errno==EINTR);close(start_pipe[1]);if(ready!=1)return 97;}
 bool result=send_output(&out);uint64_t elapsed=mono_ms()-began;
 close(fd[0]);if(reader<0 && mode!=6)close(fd[1]);int status=0;if(reader>0)waitpid(reader,&status,0);
 printf("mode=%d result=%d elapsed_ms=%llu polls=%d writes=%d reader_ok=%d\n",mode,result,(unsigned long long)elapsed,poll_calls,write_calls,reader<0 || (WIFEXITED(status)&&WEXITSTATUS(status)==0));
 free(out.b);
 return 0;
}
'''


class SendOutputBudgetTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.tmp = tempfile.TemporaryDirectory(prefix='gc-owned-send-budget-')
        source = Path(cls.tmp.name) / 'send.c'
        cls.binary = Path(cls.tmp.name) / 'send'
        source.write_text(PRELUDE + '#define GC_HELPER_V3_SEND_BUDGET 1\n' + production_send_body() + SUFFIX)
        subprocess.run([os.environ.get('CC', 'cc'), '-std=c11', '-D_POSIX_C_SOURCE=200809L',
                        '-O2', str(source), '-o', str(cls.binary)], check=True, capture_output=True)

        legacy_source = Path(cls.tmp.name) / 'legacy.c'
        cls.legacy_binary = Path(cls.tmp.name) / 'legacy'
        legacy_source.write_text(PRELUDE + production_send_body() + SUFFIX)
        subprocess.run([os.environ.get('CC', 'cc'), '-std=c11', '-D_POSIX_C_SOURCE=200809L',
                        '-O2', str(legacy_source), '-o', str(cls.legacy_binary)], check=True, capture_output=True)

    @classmethod
    def tearDownClass(cls):
        cls.tmp.cleanup()

    def run_case(self, mode):
        result = subprocess.run([str(self.binary), str(mode)], check=True,
                                capture_output=True, text=True, timeout=10)
        return {k: int(v) for k, v in (part.split('=') for part in result.stdout.split())}

    def test_only_v3_entry_enables_remaining_budget_sender(self):
        entry = (HERE / 'observer-v3.c').read_text()
        self.assertLess(entry.index('#define GC_HELPER_V3_SEND_BUDGET 1'),
                        entry.index('#include "observer.c"'))
        self.assertNotIn('#define GC_HELPER_V3_SEND_BUDGET',
                         (HERE / 'observer.c').read_text())

    def test_owned_backpressure_250ms_within_original_budget(self):
        row = self.run_case(1)
        self.assertEqual(row['result'], 1, row)
        self.assertEqual(row['reader_ok'], 1, row)
        self.assertGreaterEqual(row['elapsed_ms'], 200, row)
        self.assertLess(row['elapsed_ms'], 1500, row)

    def test_reader_pause_starts_after_delayed_parent_budget(self):
        row = self.run_case(9)
        self.assertEqual(row['result'], 1, row)
        self.assertEqual(row['reader_ok'], 1, row)
        self.assertGreaterEqual(row['elapsed_ms'], 200, row)

    def test_interrupted_poll_stays_in_original_budget(self):
        self.assertEqual(self.run_case(2)['result'], 1)

    def test_interrupted_write_stays_in_original_budget(self):
        self.assertEqual(self.run_case(3)['result'], 1)

    def test_nonblocking_write_retries_readiness_within_budget(self):
        self.assertEqual(self.run_case(4)['result'], 1)

    def test_remaining_200ms_does_not_restart_ten_second_budget(self):
        row = self.run_case(5)
        self.assertEqual(row['result'], 0, row)
        self.assertGreaterEqual(row['elapsed_ms'], 150, row)
        self.assertLess(row['elapsed_ms'], 700, row)

    def test_deadline_after_poll_prevents_new_write(self):
        row = self.run_case(7)
        self.assertEqual(row['result'], 0, row)
        self.assertEqual(row['writes'], 0, row)

    def test_deadline_at_final_write_cannot_report_success(self):
        self.assertEqual(self.run_case(8)['result'], 0)

    def test_legacy_reference_remains_exact_100ms_behavior(self):
        result = subprocess.run([str(self.legacy_binary), '1'], check=True,
                                capture_output=True, text=True, timeout=10)
        row = {k: int(v) for k, v in (part.split('=') for part in result.stdout.split())}
        self.assertEqual(row['result'], 0, row)
        self.assertLess(row['elapsed_ms'], 220, row)

    def test_closed_owned_peer_still_fails(self):
        self.assertEqual(self.run_case(6)['result'], 0)


if __name__ == '__main__':
    unittest.main()
