/* Persistent M/R full producer fixture. Real owned managed process trees and
 * original Linux descriptors; only ENV failure and provider context synthetic.
 * Enumeration covers the explicitly owned fixture, never the whole host. */
#define HELPER_SOURCE_REVISION "31731c940f4347f293869664a337e65f98b2c2e4"
#define CENSUS_KERNEL_MAIN census_mr_child_main
#include "census-v3-kernel-test.c"
#include "census-continuation.h"
#include "census-json.h"
static struct owned_tree managed,managed_birth;
static pid_t plain;static int plain_gate;static unsigned step;
static bool managed_dead,birth_dead,plain_dead,normalization,root_dead,late_final;
static const char *negative;static struct census_global_source *owned_source;static int injected_extra=-1;
static int mr_children(int argc,char **argv) {
 (void)argc;
 if(!strcmp(argv[1],"owned-leaf")) {
  int ready=atoi(argv[2]),control=atoi(argv[3]);assert(write(ready,"b",1)==1);char cmd;bool changed_name=false;
  while(read(control,&cmd,1)==1) {
   if(cmd=='c') {changed_name=!changed_name;assert(!prctl(PR_SET_NAME,changed_name?"mr-changed-a":"mr-changed-b"));assert(write(ready,"n",1)==1);continue;}
   break;
  }
  close(ready);close(control);return 0;
 }
 assert(!strcmp(argv[1],"owned-root"));
 int ready=atoi(argv[2]),control=atoi(argv[3]),lr[2],lc[2];assert(!pipe(lr) && !pipe(lc));pid_t leaf=fork();assert(leaf>=0);
 if(!leaf) {
  close(lr[0]);close(lc[1]);close(ready);close(control);char a[32],b[32];
  snprintf(a,sizeof a,"%d",lr[1]);snprintf(b,sizeof b,"%d",lc[0]);
  char *args[]={argv[0],"owned-leaf",a,b,NULL};execve(argv[0],args,environ);_exit(2);
 }
 close(lr[1]);close(lc[0]);char ack;assert(read(lr[0],&ack,1)==1 && ack=='b');
 assert(write(ready,&leaf,sizeof leaf)==sizeof leaf);bool alive=true;char cmd;
 while(read(control,&cmd,1)==1) {
  if(cmd=='c' && alive) {assert(write(lc[1],"c",1)==1);assert(read(lr[0],&ack,1)==1 && ack=='n');assert(write(ready,"n",1)==1);continue;}
  if((cmd=='l' || cmd=='r') && alive) {assert(write(lc[1],"x",1)==1);close(lc[1]);int status;assert(waitpid(leaf,&status,0)==leaf && WIFEXITED(status) && WEXITSTATUS(status)==0);alive=false;}
  if(cmd=='l') assert(write(ready,"d",1)==1);
  if(cmd=='r') break;
 }
 assert(!alive);close(lr[0]);close(ready);close(control);return 0;
}
static struct owned_tree mr_named_tree(const char *self,const char *sid) {
 int ready[2],control[2];assert(!pipe(ready) && !pipe(control));pid_t root=fork();assert(root>=0);
 if(!root) {
  close(ready[0]);close(control[1]);char a[32],b[32],owner[80];
  snprintf(a,sizeof a,"%d",ready[1]);snprintf(b,sizeof b,"%d",control[0]);snprintf(owner,sizeof owner,"GC_SESSION_ID=%s",sid);
  char *args[]={(char *)self,"owned-root",a,b,NULL};
  char *env[]={owner,"GC_CITY_PATH=/disposable-v3-test","GC_TEMPLATE=fixture.worker","GC_RUNTIME_EPOCH=1","GC_INSTANCE_TOKEN=private-owned-v3-token",NULL};
  execve(self,args,env);_exit(2);
 }
 close(ready[1]);close(control[0]);pid_t leaf;assert(read(ready[0],&leaf,sizeof leaf)==sizeof leaf);
 return (struct owned_tree){root,leaf,control[1],ready[0]};
}
#ifndef GC_MR_BASELINE
static bool mr_roots_only(void *unused,struct census_budget *b,struct census_pid_list *list) {
 (void)unused;uint32_t pids[3]={(uint32_t)getpid(),(uint32_t)managed.root,(uint32_t)managed_birth.root};
 for(unsigned i=0;i<3;i++) for(unsigned k=i+1;k<3;k++) if(pids[k]<pids[i]) {uint32_t v=pids[i];pids[i]=pids[k];pids[k]=v;}
 for(unsigned i=0;i<3;i++) if(!census_pid_append(b,list,pids[i])) return false;
 return true;
}
#endif
static unsigned birth_env_calls;
static int mr_env(uint32_t pid) {
 if(step==(late_final?CENSUS_MAX_SCANS-1u:2u) && pid==(uint32_t)managed.leaf) return ESRCH;
 if(!normalization && step==(late_final?CENSUS_MAX_SCANS-1u:6u) && pid==(uint32_t)managed_birth.leaf && ++birth_env_calls==2) {
  retire_leaf(&managed_birth);birth_dead=true;return ESRCH;
 }
 return 0;
}
static bool mr_enum(void *unused,struct census_budget *b,struct census_pid_list *list) {
 (void)unused;step++;
 /* Real owned mutable comm at every earlier seal prevents early convergence.
  * No fake scans/errors/clock: retain same child/start/UID/GC identity and wait
  * for its actual PR_SET_NAME acknowledgement before recording that scan. */
 if(late_final && step>=4 && step<CENSUS_MAX_SCANS && (step-1)%3==0) {
  assert(write(managed.control,"c",1)==1);char ack;assert(read(managed.ready,&ack,1)==1 && ack=='n');
 }
 if(step==2 && negative && !strcmp(negative,"comm-before-env")) {assert(write(managed.control,"c",1)==1);char ack;assert(read(managed.ready,&ack,1)==1 && ack=='n');}
 if(step==(late_final?CENSUS_MAX_SCANS:3u)) {
  if(!negative || strcmp(negative,"live")) {retire_leaf(&managed);managed_dead=true;}
  #ifndef GC_MR_BASELINE
  if(negative && !late_final && strncmp(negative,"r-",2)) {
   struct census_capture_fault *f=&owned_source->errors[0].raw;
   if(!strcmp(negative,"wrongfd")) {int old=(int)f->retained_pidfd_plus_one-1;int replacement=process_pidfd(managed.root);assert(replacement>=0 && census_fd_take(b,replacement));f->retained_pidfd_plus_one=(unsigned)replacement+1;census_fd_close(b,old);injected_extra=replacement;}
   else if(!strcmp(negative,"wrongdir")) {int old=(int)f->retained_procdir_plus_one-1;char path[32];snprintf(path,sizeof path,"%u",(unsigned)managed.root);int replacement=openat(ev.procfd,path,O_RDONLY|O_DIRECTORY|O_CLOEXEC);assert(replacement>=0 && census_fd_take(b,replacement));f->retained_procdir_plus_one=(unsigned)replacement+1;census_fd_close(b,old);injected_extra=replacement;}
   else if(!strcmp(negative,"binding")) f->retained_binding_verified=false;
   else if(!strcmp(negative,"uid")) f->retained_uids[0]++;
   else if(!strcmp(negative,"secondpass")) f->fields_pass=2;
   else if(!strcmp(negative,"rootloss")) {retire_tree(&managed);root_dead=true;}
   else if(!strcmp(negative,"deadline")) ev.monotonic_start.tv_sec-=11;
   else if(!strcmp(negative,"budget")) b->fd_failed=true;
   else if(!strcmp(negative,"numeric")) owned_source->history->scans[1].numeric_enumeration_complete=false;
   else assert(!strcmp(negative,"live"));
  }
  #endif
 }
 if(step==(normalization?6u:5u)) {assert(write(plain_gate,"x",1)==1);close(plain_gate);int status;assert(waitpid(plain,&status,0)==plain);plain_dead=true;}
 #ifndef GC_MR_BASELINE
 if(step==5 && negative && !strncmp(negative,"r-",2)) {
  struct census_history_row *row=census_history_find(&owned_source->history->scans[3],plain);assert(row);
  struct census_capture_fault *f=&row->raw_fault;
  if(!strcmp(negative,"r-wrongfd")) {int old=(int)f->retained_pidfd_plus_one-1;int replacement=process_pidfd(managed.root);assert(replacement>=0 && census_fd_take(b,replacement));f->retained_pidfd_plus_one=(unsigned)replacement+1;census_fd_close(b,old);injected_extra=replacement;}
  else if(!strcmp(negative,"r-wrongdir")) {int old=(int)f->retained_procdir_plus_one-1;char path[32];snprintf(path,sizeof path,"%u",(unsigned)managed.root);int replacement=openat(ev.procfd,path,O_RDONLY|O_DIRECTORY|O_CLOEXEC);assert(replacement>=0 && census_fd_take(b,replacement));f->retained_procdir_plus_one=(unsigned)replacement+1;census_fd_close(b,old);injected_extra=replacement;}
  else if(!strcmp(negative,"r-uid")) f->retained_uids[0]++;
  else if(!strcmp(negative,"r-reuse")) f->retained_start++;
  else if(!strcmp(negative,"r-protected")) row->identity.declared_root=true;
  else assert(false);
 }
 #endif
 if(normalization && step==6) {retire_leaf(&managed_birth);birth_dead=true;}
 uint32_t pids[6];size_t n=0;pids[n++]=getpid();pids[n++]=managed.root;pids[n++]=managed_birth.root;
 if(!managed_dead) pids[n++]=managed.leaf;
 if(!normalization && step>=(late_final?CENSUS_MAX_SCANS-3u:4u) && (step<=(late_final?CENSUS_MAX_SCANS-1u:6u) || !birth_dead)) pids[n++]=managed_birth.leaf;
 if(step>=4 && !plain_dead) pids[n++]=plain;
 for(size_t i=0;i<n;i++) for(size_t j=i+1;j<n;j++) if(pids[j]<pids[i]) {uint32_t v=pids[i];pids[i]=pids[j];pids[j]=v;}
 struct census_pid_list numeric={0};
 if(!census_pid_enumerate(b,&numeric)) {census_pid_clear(b,&numeric);return false;}
 for(unsigned k=0;k<2;k++) {
  uint32_t retired=k?(uint32_t)managed_birth.leaf:(uint32_t)managed.leaf;
  bool present=false;for(size_t i=0;i<numeric.n;i++) if(numeric.p[i]==retired) present=true;
  if(k?birth_dead:managed_dead) assert(!present);
  else assert(present);
 }
 if(plain_dead) {for(size_t i=0;i<numeric.n;i++) assert(numeric.p[i]!=(uint32_t)plain);}
 census_pid_clear(b,&numeric);
 for(size_t i=0;i<n;i++) if(!census_pid_append(b,list,pids[i])) return false;
 return !(negative && !strcmp(negative,"numeric") && step==3);
}
int main(int argc,char **argv) {
 if(argc==4) return mr_children(argc,argv);
 normalization=argc==2 && !strcmp(argv[1],"normalization");
 if(argc==2 && !strncmp(argv[1],"negative-",9)) negative=argv[1]+9;
 late_final=negative && !strcmp(negative,"late-final");
 #ifdef GC_MR_BASELINE
 negative="baseline67";
 #endif
 struct rlimit limit={32,32};assert(!setrlimit(RLIMIT_NOFILE,&limit));
 init_evidence();ssize_t ns=readlink("/proc/self/ns/pid",ev.ns,sizeof ev.ns-1);assert(ns>0);ev.ns[ns]=0;strcpy(ev.binding.ns,ev.ns);
 managed=mr_named_tree(argv[0],"v3-owned-m");managed_birth=mr_named_tree(argv[0],"v3-owned-birth");
 int gate[2];assert(!pipe(gate));plain=fork();assert(plain>=0);
 if(!plain) {close(gate[1]);close(managed.control);close(managed.ready);close(managed_birth.control);close(managed_birth.ready);char x;assert(read(gate[0],&x,1)==1);close(gate[0]);_exit(0);}
 close(gate[0]);plain_gate=gate[1];
 struct census_budget budget={.limit=16777216};
 struct census_global_source *s=census_global_new(&budget,NULL,mr_enum,census_fixed_capture);assert(s);
 struct census_resolution_ledger *r=census_resolution_new(&budget);assert(r);
 owned_source=s;
 census_test_environ_error=mr_env;struct census_round_journal j={0};
 enum census_round_result result=census_choose_proven_continuation(s,r,&j);
 fprintf(stderr,"M/R result=%d scans=%u errors=%u resolutions=%u proofs=%u certs=%u denied=%d fdpeak=%u\n",result,j.n,s->errors_n,r->resolutions_n,r->proofs_n,r->certificates_n,r->denied,budget.fd_peak);
 if(result!=CENSUS_ROUNDS_SELECTED_PENDING_PROOF || j.n!=(normalization?7u:10u)) {
  #ifndef GC_MR_BASELINE
  for(unsigned k=0;k<s->errors_n;k++) fprintf(stderr,"err%u reason=%s pid=%u stage=%u binding=%d\n",k+1,s->errors[k].reason,s->errors[k].raw.pid,s->errors[k].raw.retained_stage,s->errors[k].raw.retained_binding_verified);
  #endif
  if(!managed_dead) retire_leaf(&managed);
  if(!birth_dead) retire_leaf(&managed_birth);
  if(!plain_dead) {write(plain_gate,"x",1);close(plain_gate);waitpid(plain,NULL,0);}
  if(late_final) {
    /* The real pre-ENV identity/custody guard refuses this unproved fault
     * immediately; do not fabricate scan31 or a post-seal terminal witness. */
    assert(result==CENSUS_ROUNDS_UNKNOWN && j.n==CENSUS_MAX_SCANS-1u &&
      !j.selected_first && !j.selected_second && !j.selected_seal);
    const struct census_history_row *prior=census_history_find(&s->history->scans[CENSUS_MAX_SCANS-3u],(uint32_t)managed.leaf);
    assert(census_row_verified(prior));bool exact=false;
    for(unsigned k=0;k<s->errors_n;k++) {
      const struct census_global_fault *e=&s->errors[k];
      if(e->raw.pid!=(uint32_t)managed.leaf || e->scan_index!=CENSUS_MAX_SCANS-1u) continue;
      assert(!strcmp(e->reason,"process_unavailable") && e->raw.start==prior->identity.start &&
        e->raw.operation && !strcmp(e->raw.operation,"environ") && e->raw.error==ESRCH &&
        !e->raw.retained_stage && !e->raw.retained_pidfd_plus_one);
      for(unsigned q=0;q<r->resolutions_n;q++) assert(r->resolutions[q].error_index!=k+1u);
      exact=true;
    }
    const struct census_history_row *root=census_history_find(&s->history->scans[j.n-1],(uint32_t)managed.root);
    assert(exact && census_row_verified(root) && root->identity.declared_root && budget.fd_peak<=32 && !budget.fd_failed);
    fprintf(stderr,"exact unproved firstENV%u/ESRCH/no-custody UNKNOWN; NO later scan or exit witness invented PASS\n",CENSUS_MAX_SCANS-1u);
  }
  if(!root_dead) retire_tree(&managed);
  retire_tree(&managed_birth);census_resolution_free(&budget,r);census_global_free(s);
  if(injected_extra>=0) census_fd_close(&budget,injected_extra);
  assert(!budget.used);assert(negative && (budget.fd_failed==!strcmp(negative,"budget")));
  close(ev.procfd);fprintf(stderr,"controlled_kernel_negative=%s UNKNOWN/oneclose/teardown0 PASS\n",negative);return 0;
 }
 #ifdef GC_MR_BASELINE
 assert(false);return 1;
 #else
 assert(!negative);
 unsigned m=0,b=0,reap=0;
 for(unsigned k=0;k<r->resolutions_n;k++) {m+=r->resolutions[k].kind==CENSUS_RESOLUTION_DEFERRED_MANAGED;b+=r->resolutions[k].kind==CENSUS_RESOLUTION_MANAGED_BIRTH;reap+=r->resolutions[k].kind==CENSUS_RESOLUTION_REAPED_BIRTH;}
 assert(m==1 && b==(normalization?0u:1u) && reap==1 && census_final_resolutions_valid(s,r,&j));
 if(normalization) assert(s->history->scans[4].receipt.classified==s->history->scans[6].receipt.classified+1);
 struct census_json out={.budget=&budget};census_json_text(&out,"{\"census\":");census_json_census(&out,s,r,&j);
 census_json_text(&out,",\"errors\":");census_json_errors(&out,s);
 uint64_t first_origin=(uint64_t)ev.monotonic_start.tv_sec*1000u+(uint64_t)ev.monotonic_start.tv_nsec/1000000u;
 census_json_printf(&out,",\"errors_total\":%u,\"duration_ms\":%" PRIu64 ",\"controller_pid\":%u",s->errors_total,mono_ms()-first_origin,(unsigned)getpid());
 /* An independently captured second root-only frame, not a rewritten first
  * frame. It has its own fresh 10s/32FD/10slot accounting and real full reads. */
 struct census_budget second_budget={.limit=16777216};
 assert(!clock_gettime(CLOCK_MONOTONIC,&ev.monotonic_start));census_test_environ_error=NULL;
 struct census_global_source *second=census_global_new(&second_budget,NULL,mr_roots_only,census_fixed_capture);
 struct census_resolution_ledger *second_ledger=census_resolution_new(&second_budget);assert(second && second_ledger);
 struct census_round_journal second_journal={0};
 assert(census_choose_proven_continuation(second,second_ledger,&second_journal)==CENSUS_ROUNDS_SELECTED_PENDING_PROOF);
 census_json_text(&out,",\"second\":{\"census\":");census_json_census(&out,second,second_ledger,&second_journal);
 census_json_text(&out,",\"errors\":");census_json_errors(&out,second);
 uint64_t second_origin=(uint64_t)ev.monotonic_start.tv_sec*1000u+(uint64_t)ev.monotonic_start.tv_nsec/1000000u;
 census_json_printf(&out,",\"errors_total\":%u,\"duration_ms\":%" PRIu64 ",\"controller_pid\":%u}}",second->errors_total,mono_ms()-second_origin,(unsigned)getpid());
 census_resolution_free(&second_budget,second_ledger);census_global_free(second);assert(!second_budget.used && !second_budget.fd_failed);
 assert(!out.failed);assert(fwrite(out.data,1,out.n,stdout)==out.n);putchar('\n');
 census_release(&budget,out.data,out.capacity);
 retire_tree(&managed);retire_tree(&managed_birth);
 census_resolution_free(&budget,r);census_global_free(s);assert(!budget.used && !budget.fd_failed);close(ev.procfd);
 fprintf(stderr,"mixed_M_originalENV_managedbirth_and_R_directreap realFD/kernel/UID/numeric proof PASS; syntheticENV/provider; descriptor singleclose/teardown0 PASS\n");return 0;
 #endif
}
