/* Strong original handle/generation negatives on TWO REAPED own children.
 * No host census, RPC, production helper, new capability or extra budget. */
#define GC_HELPER_TEST
#define HELPER_SOURCE_REVISION "31731c940f4347f293869664a337e65f98b2c2e4"
#define main original_custody_observer_main
#include "observer.c"
#undef main
#include "census-v3-reader.h"
#include "census-continuation.h"
#include <assert.h>
#include <sys/resource.h>
#include <sys/wait.h>
static unsigned actual_fds(void) {
 unsigned n=0;for(int fd=0;fd<32;fd++) if(fcntl(fd,F_GETFD)>=0) n++;return n;
}
int main(void) {
 struct rlimit limit={32,32};assert(!setrlimit(RLIMIT_NOFILE,&limit));
 memset(&ev,0,sizeof ev);ev.procfd=open("/proc",O_RDONLY|O_DIRECTORY|O_CLOEXEC);assert(ev.procfd>=0);
 ev.binding.pid=getpid();ev.trusted_kernel=true;assert(!clock_gettime(CLOCK_MONOTONIC,&ev.monotonic_start));
 ssize_t ns=readlink("/proc/self/ns/pid",ev.ns,sizeof ev.ns-1);assert(ns>0);ev.ns[ns]=0;strcpy(ev.binding.ns,ev.ns);
 pid_t children[2];int gates[2];
 for(unsigned i=0;i<2;i++) {int gate[2];assert(!pipe(gate));children[i]=fork();assert(children[i]>=0);
  if(!children[i]) {close(gate[1]);char c;assert(read(gate[0],&c,1)==1);close(gate[0]);_exit(0);}
  close(gate[0]);gates[i]=gate[1];
 }
 struct census_budget budget={.limit=16777216};struct census_owned_identity identities[2]={{0}};
 struct census_capture_fault captures[2]={{0}};
 for(unsigned i=0;i<2;i++) assert(census_capture_bounded_custodied(children[i],&budget,&identities[i],&captures[i],0,NULL,true));
 int original=(int)captures[0].retained_pidfd_plus_one-1,other=(int)captures[1].retained_pidfd_plus_one-1;
 /* Resource constructor: genuine successful pidfd_open against a LIVE own PID,
  * meter take fails under the existing32 bound and still closes the new FD. */
 unsigned before=actual_fds();struct census_budget probe_budget={.limit=16777216};
 assert(census_fd_initialize(&probe_budget));probe_budget.fd_live=32;
 assert(!census_original_no_pid_probe(&probe_budget,children[1]) && probe_budget.fd_failed && probe_budget.fd_live==32);
 assert(actual_fds()==before && !probe_budget.used);
 for(unsigned i=0;i<32;i++) assert(!probe_budget.fd_owners[i].active || !probe_budget.fd_owners[i].owned);
 for(unsigned i=0;i<2;i++) {assert(write(gates[i],"x",1)==1);close(gates[i]);int status;assert(waitpid(children[i],&status,0)==children[i]);}
 assert(census_original_reaped(&budget,&captures[0],children[0],identities[0].start));
 captures[0].retained_pidfd_plus_one=(unsigned)other+1;
 assert(!census_original_reaped(&budget,&captures[0],children[0],identities[0].start)); /* original procdir remains unchanged */
 captures[0].retained_pidfd_plus_one=(unsigned)original+1;
 uint64_t old_generation=census_fd_owner_find(&budget,original)->generation;
 census_fd_close(&budget,original);
 assert(dup2(other,original)==original && census_fd_take(&budget,original));
 assert(census_fd_owner_find(&budget,original)->generation!=old_generation);
 assert(!census_original_reaped(&budget,&captures[0],children[0],identities[0].start)); /* SAME numericFD, OTHER reaped pidfd */
 census_capture_custody_close(&budget,&captures[0]);
 assert(fcntl(original,F_GETFD)>=0); /* release did not close a replacement generation */
 census_fd_close(&budget,original);census_capture_custody_close(&budget,&captures[1]);
 for(unsigned i=0;i<2;i++) census_identity_clear(&budget,&identities[i]);
 for(unsigned i=0;i<32;i++) assert(!budget.fd_owners[i].active || !budget.fd_owners[i].owned);
 assert(!budget.used && !budget.fd_failed);close(ev.procfd);
 printf("original positive PASS; OTHER-REAPEDpidfd with ORIGINALprocdir REFUSED; sameFDnumberABA REFUSED; take-failure successfulopen closed/actualFDcountunchanged; immutablehandle singleclose/teardown0 PASS peakFD=%u\n",budget.fd_peak);
 return 0;
}
