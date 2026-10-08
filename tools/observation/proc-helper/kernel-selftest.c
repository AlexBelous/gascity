/* Disposable own processes only: no host census, database, unit or capability
 * operations. Includes the actual reader adapter, not synthetic ESRCH. */
#define GC_HELPER_TEST
#define main observer_fixture_main
#include "observer.c"
#undef main
#include <assert.h>
#include <sys/resource.h>
#include <sys/wait.h>

static pid_t owned_child;
static int release_child;
static void exit_owned_child(void) {
  test_environ_opened=NULL;
  char done='x'; assert(write(release_child,&done,1)==1);
  close(release_child);
  int status; assert(waitpid(owned_child,&status,0)==owned_child);
  assert(WIFEXITED(status) && WEXITSTATUS(status)==0);
}
static void start_child(const char *self,bool unreadable,bool managed) {
  int ready[2], control[2]; assert(!pipe(ready)); assert(!pipe(control));
  owned_child=fork();assert(owned_child>=0);
  if (!owned_child) {
    close(ready[0]);close(control[1]);
    char a[32],b[32]; snprintf(a,sizeof a,"%d",ready[1]);snprintf(b,sizeof b,"%d",control[0]);
    char *args[]={(char *)self,"child",a,b,unreadable?"private":"readable",NULL};
    char *plain[]={"TEST_NON_GC=1",NULL};
    char *gc[]={"GC_SESSION_ID=owned-test","GC_CITY_PATH=/disposable-test",
      "GC_TEMPLATE=fixture.worker","GC_RUNTIME_EPOCH=1","GC_INSTANCE_TOKEN=private-test-token",NULL};
    execve(self,args,managed?gc:plain);_exit(2);
  }
  close(ready[1]);close(control[0]);release_child=control[1];
  char byte;assert(read(ready[0],&byte,1)==1);close(ready[0]);
}
static void reset_evidence(void) {
  memset(&ev,0,sizeof ev);ev.complete=true;ev.processfd=-1;ev.active_pidfd=-1;
  ev.procfd=open("/proc",O_RDONLY|O_DIRECTORY|O_CLOEXEC);assert(ev.procfd>=0);
  ev.binding.pid=(uint32_t)getpid();ev.trusted_kernel=true;ev.scan_index=1;
  assert(!clock_gettime(CLOCK_MONOTONIC,&ev.monotonic_start));
}
static struct process inspect_child(bool expected) {
  struct scan s={0};struct process p={.pid=(uint32_t)owned_child};
  assert(read_process(&s,&p)==expected);return p;
}
static void free_row(struct process *p) {
  free(p->sid);free(p->city);free(p->template);free(p->name);
}
int main(int argc,char **argv) {
  if (argc==5 && !strcmp(argv[1],"child")) {
    if (!strcmp(argv[4],"private")) assert(!prctl(PR_SET_DUMPABLE,0,0,0,0));
    char ready='r';assert(write(atoi(argv[2]),&ready,1)==1);close(atoi(argv[2]));
    char done;assert(read(atoi(argv[3]),&done,1)==1);return 0;
  }
  struct utsname u;assert(!uname(&u));assert(!strncmp(u.release,"6.8.",4));
  struct rlimit limit={32,32};assert(!setrlimit(RLIMIT_NOFILE,&limit));
  /* Successful read, closed pidfd, later exit: fresh real kernel no-PID proof. */
  reset_evidence();start_child(argv[0],false,false);
  struct process p=inspect_child(true);uint64_t old_start=p.start;
  assert(p.valid && p.pidfd_bound && !p.absent);exit_owned_child();
  assert(missing_proof(&p,NULL,2)>0 && p.absent);
  assert(ev.proofs[0].start==old_start && !strcmp(ev.proofs[0].method,"pidfd_no_pid"));
  free_row(&p);close(ev.procfd);
  /* Open environ inode while alive, then reap: real proc read ESRCH + pidfd. */
  reset_evidence();start_child(argv[0],false,false);test_environ_opened=exit_owned_child;
  p=inspect_child(true);assert(p.absent && ev.errors_n==1 && ev.unresolved==0);
  assert(ev.errors[0].error==ESRCH && ev.errors[0].resolved_by==1);
  assert(!strcmp(ev.proofs[0].method,"pidfd_exited"));free_row(&p);close(ev.procfd);
  /* Reaped before first stat: null-start enumerated absence, never retirement. */
  reset_evidence();start_child(argv[0],false,false);exit_owned_child();
  p=inspect_child(true);assert(!p.start && p.absent);
  assert(ev.proofs[0].kind==CENSUS_ENUMERATED_ABSENT && !ev.proofs[0].start);
  assert(ev.errors[0].error==ENOENT && ev.errors[0].resolved_by==1);close(ev.procfd);
  /* A live unreadable process is not absence even if it later exits. */
  reset_evidence();start_child(argv[0],true,false);
  p=inspect_child(false);assert(!p.absent && ev.unresolved>0 && !ev.proofs_n);
  exit_owned_child();free_row(&p);close(ev.procfd);
  /* A previously known GC identity is not removed by a no-PID witness. */
  reset_evidence();start_child(argv[0],false,true);p=inspect_child(true);
  exit_owned_child();assert(missing_proof(&p,NULL,2)==0 && !p.absent);
  free_row(&p);close(ev.procfd);
  /* Serial inspection, constant live descriptor demand under NOFILE32. */
  for (unsigned i=0;i<300;i++) {
    reset_evidence();start_child(argv[0],false,false);p=inspect_child(true);
    assert(!p.absent && !ev.proofs_n);exit_owned_child();
    assert(missing_proof(&p,NULL,2)>0);free_row(&p);close(ev.procfd);
  }
  printf("actual kernel=%s; owned-child exit/initial-absence/live-permission/GC-loss/300-serial NOFILE32: PASS\n",u.release);
  return 0;
}
