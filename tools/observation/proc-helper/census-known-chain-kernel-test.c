/* Owned-child kernel controls only. No host enumeration or provider claim. */
#define CENSUS_KERNEL_MAIN unused_owned_kernel_main
#include "census-v3-kernel-test.c"
#include "census-continuation.h"
#include "census-json.h"

struct family { pid_t root, grandparent, parent, child; int control, ready; unsigned scans,retire_at; bool retired,deep; };
static bool xread(int fd, void *v, size_t n) { return read(fd,v,n)==(ssize_t)n; }
static bool xwrite(int fd, const void *v, size_t n) { return write(fd,v,n)==(ssize_t)n; }

static int child_role(int argc, char **argv) {
  if(argc!=4) return 2;
  int ready=atoi(argv[2]), ctl=atoi(argv[3]);
  if(!strcmp(argv[1],"owned-child")) { char c='c';
    if(!xwrite(ready,&c,1)) return 3;
    if(!xread(ctl,&c,1)) return 4;
    return 0;
  }
  if(!strcmp(argv[1],"owned-parent")) {
    int cr[2],cc[2];if(pipe(cr)||pipe(cc)) return 5;
    pid_t child=fork();if(child<0) return 6;
    if(!child) {close(cr[0]);close(cc[1]);close(ready);close(ctl);
      char a[32],b[32];snprintf(a,sizeof a,"%d",cr[1]);snprintf(b,sizeof b,"%d",cc[0]);
      char *args[]={argv[0],"owned-child",a,b,NULL};execve(argv[0],args,environ);_exit(7);
    }
    close(cr[1]);close(cc[0]);char ack;
    if(!xread(cr[0],&ack,1)||ack!='c'||!xwrite(ready,&child,sizeof child)) return 8;
    close(cr[0]);if(!xread(ctl,&ack,1)) ack='x';
    if(!xwrite(cc[1],"x",1)) return 9;
    close(cc[1]);int status=0;
    if(waitpid(child,&status,0)!=child||!WIFEXITED(status)||WEXITSTATUS(status)) return 10;
    return 0;
  }
  if(!strcmp(argv[1],"owned-grandparent")) {
    int pr[2],pc[2];if(pipe(pr)||pipe(pc)) return 71;
    pid_t parent=fork();if(parent<0) return 72;
    if(!parent) {close(pr[0]);close(pc[1]);close(ready);close(ctl);
      char a[32],b[32];snprintf(a,sizeof a,"%d",pr[1]);snprintf(b,sizeof b,"%d",pc[0]);
      char *args[]={argv[0],"owned-parent",a,b,NULL};execve(argv[0],args,environ);_exit(73);
    }
    close(pr[1]);close(pc[0]);pid_t child=0;
    if(!xread(pr[0],&child,sizeof child)) return 74;
    close(pr[0]);pid_t ids[2]={parent,child};
    if(!xwrite(ready,ids,sizeof ids)) return 75;
    char cmd;if(!xread(ctl,&cmd,1)) cmd='x';
    if(!xwrite(pc[1],"x",1)) return 76;
    close(pc[1]);int status=0;
    if(waitpid(parent,&status,0)!=parent||!WIFEXITED(status)||WEXITSTATUS(status)) return 77;
    return 0;
  }
  if(!strcmp(argv[1],"owned-root") || !strcmp(argv[1],"owned-deep-root")) {
    bool deep=!strcmp(argv[1],"owned-deep-root");
    int pr[2],pc[2];if(pipe(pr)||pipe(pc)) return 11;
    pid_t parent=fork();if(parent<0) return 12;
    if(!parent) {close(pr[0]);close(pc[1]);close(ready);close(ctl);
      char a[32],b[32];snprintf(a,sizeof a,"%d",pr[1]);snprintf(b,sizeof b,"%d",pc[0]);
      char *args[]={argv[0],deep?"owned-grandparent":"owned-parent",a,b,NULL};execve(argv[0],args,environ);_exit(13);
    }
    close(pr[1]);close(pc[0]);pid_t descendants[2]={0};
    if(!xread(pr[0],descendants,deep?sizeof descendants:sizeof descendants[0])) return 14;
    close(pr[0]);pid_t ids[4]={getpid(),parent,descendants[0],descendants[1]};
    if(!deep) ids[2]=descendants[0];
    if(!xwrite(ready,ids,deep?sizeof ids:3*sizeof ids[0])) return 15;
    char cmd;if(!xread(ctl,&cmd,1)) cmd='x';
    if(!xwrite(pc[1],"x",1)) return 16;
    close(pc[1]);int status=0;
    if(waitpid(parent,&status,0)!=parent||!WIFEXITED(status)||WEXITSTATUS(status)) return 17;
    cmd='d';if(!xwrite(ready,&cmd,1)) return 18;
    /* Keep the owned managed root live through all capture/validation. */
    (void)xread(ctl,&cmd,1);
    return 0;
  }
  return 19;
}

static bool enumerate_family(void *context,struct census_budget *b,struct census_pid_list *list) {
  struct family *f=context;f->scans++;
  unsigned terminal=f->retire_at?f->retire_at:2;
  if(f->scans==terminal && !f->retired) {
    char ack=0;if(!xwrite(f->control,"x",1)||!xread(f->ready,&ack,1)||ack!='d') return false;
    f->retired=true;
  }
  uint32_t pids[5]={(uint32_t)getpid(),(uint32_t)f->root,
    (uint32_t)(f->deep?f->grandparent:f->parent),
    (uint32_t)(f->deep?f->parent:f->child),(uint32_t)f->child};
  for(unsigned i=0;i<(f->scans<=terminal?(f->deep?5u:4u):2u);i++)
    if(!census_pid_append(b,list,pids[i])) return false;
  return true;
}
static bool capture_family(void *context,uint32_t pid,struct census_budget *b,
    struct census_owned_identity *out,struct census_capture_fault *fault) {
  struct family *f=context;bool ok=census_capture_bounded_evidenced(pid,b,out,fault);
  if(ok && pid==(uint32_t)f->root) out->declared_root=true;
  return ok;
}
static int reverse_control(const char *self) {
  int ready[2],ctl[2];if(pipe(ready)||pipe(ctl)) return 50;
  pid_t root=fork();if(root<0) return 51;
  if(!root) {close(ready[0]);close(ctl[1]);char a[32],b[32];
    snprintf(a,sizeof a,"%d",ready[1]);snprintf(b,sizeof b,"%d",ctl[0]);
    char *args[]={(char *)self,"owned-root",a,b,NULL};
    char *env[]={"GC_SESSION_ID=private-family", "GC_CITY_PATH=/private-fixture",
      "GC_TEMPLATE=fixture.worker","GC_RUNTIME_EPOCH=1","GC_INSTANCE_TOKEN=private-value",NULL};
    execve(self,args,env);_exit(52);
  }
  close(ready[1]);close(ctl[0]);struct family f={.control=ctl[1],.ready=ready[0]};
  pid_t ids[3]={0};int rc=0;
  if(!xread(f.ready,ids,sizeof ids)) {rc=53;goto done;}
  f.root=ids[0];f.parent=ids[1];f.child=ids[2];
  init_evidence();struct census_budget b={.limit=16777216};
  struct census_global_source *s=census_global_new(&b,&f,enumerate_family,capture_family);
  struct census_resolution_ledger *r=census_resolution_new(&b);
  if(!s||!r) {rc=54;goto cleanup;}
  struct census_round_journal j={0};
  if(!census_continuation_capture(s,r,&j,0,CENSUS_INITIAL)) {rc=55;goto cleanup;}
  struct census_round_scan receipt={.scan_id=2,.round=1,.kind=CENSUS_CLOSING};
  (void)census_global_scan_record(s,2,1,CENSUS_CLOSING,&receipt);
  if(s->history->n!=2 || s->errors_n!=2) {rc=56;goto cleanup;}
  j.scans[j.n++]=receipt;
  struct census_global_fault swap=s->errors[0];s->errors[0]=s->errors[1];s->errors[1]=swap;
  if(s->errors[0].raw.pid!=(uint32_t)f.child || s->errors[1].raw.pid!=(uint32_t)f.parent ||
      !census_continuation_gate(s,r) || r->certificates_n!=2 || r->proofs_n!=4 ||
      r->resolutions_n!=2 || r->resolutions[0].error_index!=2 ||
      r->resolutions[1].error_index!=1 ||
      !census_resolution_prefix_valid(s,r)) {rc=57;goto cleanup;}
  printf("reverse_raw_order=child_first parent_second bounded_two_passes:PASS topological_certificates:PASS\n");
cleanup:
  census_resolution_free(&b,r);census_global_free(s);close(ev.procfd);
  if(b.used||b.fd_failed||b.fd_peak>32) rc=58;
done:
  if(!f.retired) {char ack=0;(void)xwrite(f.control,"x",1);(void)xread(f.ready,&ack,1);}
  close(f.control);close(f.ready);int status=0;
  if(waitpid(root,&status,0)!=root||!WIFEXITED(status)||WEXITSTATUS(status)) rc=59;
  return rc;
}
static int unequal_prior_control(const char *self) {
  int ready[2],ctl[2];if(pipe(ready)||pipe(ctl)) return 60;
  pid_t root=fork();if(root<0) return 61;
  if(!root) {close(ready[0]);close(ctl[1]);char a[32],b[32];
    snprintf(a,sizeof a,"%d",ready[1]);snprintf(b,sizeof b,"%d",ctl[0]);
    char *args[]={(char *)self,"owned-root",a,b,NULL};
    char *env[]={"GC_SESSION_ID=private-family", "GC_CITY_PATH=/private-fixture",
      "GC_TEMPLATE=fixture.worker","GC_RUNTIME_EPOCH=1","GC_INSTANCE_TOKEN=private-value",NULL};
    execve(self,args,env);_exit(62);
  }
  close(ready[1]);close(ctl[0]);struct family f={.control=ctl[1],.ready=ready[0],.retire_at=3};
  pid_t ids[3]={0};int rc=0;
  if(!xread(f.ready,ids,sizeof ids)) {rc=63;goto done;}
  f.root=ids[0];f.parent=ids[1];f.child=ids[2];
  init_evidence();struct census_budget b={.limit=16777216};
  struct census_global_source *s=census_global_new(&b,&f,enumerate_family,capture_family);
  struct census_resolution_ledger *r=census_resolution_new(&b);
  if(!s||!r) {rc=64;goto cleanup;}
  struct census_round_journal j={0};
  enum census_round_result result=census_choose_proven_continuation(s,r,&j);
  if(result!=CENSUS_ROUNDS_SELECTED_PENDING_PROOF || j.n!=4 ||
      r->certificates_n!=2 || r->certificates[0].prior_scan!=2 ||
      r->certificates[1].prior_scan!=2 ||
      !census_final_resolutions_valid(s,r,&j)) {rc=65;goto cleanup;}
  r->certificates[0].prior_scan=1; /* Same full parent suffix was live in scan 2. */
  if(!census_certificates_history_valid(s,r) || !census_final_resolutions_valid(s,r,&j)) {rc=66;goto cleanup;}
  struct census_history_row *intermediate=census_history_find(&s->history->scans[1],f.parent);
  if(!intermediate) {rc=67;goto cleanup;}
  uint32_t uid=intermediate->identity.uids[0];intermediate->identity.uids[0]++;
  bool changed_intermediate=census_certificates_history_valid(s,r);
  intermediate->identity.uids[0]=uid;
  if(changed_intermediate || !census_final_resolutions_valid(s,r,&j)) {rc=68;goto cleanup;}
  printf("unequal_prior=parent:1 child:2 exact_suffix:PASS changed_intermediate_identity:DENY\n");
cleanup:
  census_resolution_free(&b,r);census_global_free(s);close(ev.procfd);
  if(b.used||b.fd_failed||b.fd_peak>32) rc=69;
done:
  if(!f.retired) {char ack=0;(void)xwrite(f.control,"x",1);(void)xread(f.ready,&ack,1);}
  close(f.control);close(f.ready);int status=0;
  if(waitpid(root,&status,0)!=root||!WIFEXITED(status)||WEXITSTATUS(status)) rc=70;
  return rc;
}
static int deep_chain_control(const char *self) {
  int ready[2],ctl[2];if(pipe(ready)||pipe(ctl)) return 80;
  pid_t root=fork();if(root<0) return 81;
  if(!root) {close(ready[0]);close(ctl[1]);char a[32],b[32];
    snprintf(a,sizeof a,"%d",ready[1]);snprintf(b,sizeof b,"%d",ctl[0]);
    char *args[]={(char *)self,"owned-deep-root",a,b,NULL};
    char *env[]={"GC_SESSION_ID=private-family", "GC_CITY_PATH=/private-fixture",
      "GC_TEMPLATE=fixture.worker","GC_RUNTIME_EPOCH=1","GC_INSTANCE_TOKEN=private-value",NULL};
    execve(self,args,env);_exit(82);
  }
  close(ready[1]);close(ctl[0]);struct family f={.control=ctl[1],.ready=ready[0],.deep=true};
  pid_t ids[4]={0};int rc=0;
  if(!xread(f.ready,ids,sizeof ids)) {rc=83;goto done;}
  f.root=ids[0];f.grandparent=ids[1];f.parent=ids[2];f.child=ids[3];
  if(!(getpid()<f.root && f.root<f.grandparent && f.grandparent<f.parent && f.parent<f.child)) {rc=84;goto done;}
  init_evidence();struct census_budget b={.limit=16777216};
  struct census_global_source *s=census_global_new(&b,&f,enumerate_family,capture_family);
  struct census_resolution_ledger *r=census_resolution_new(&b);
  if(!s||!r) {rc=85;goto cleanup;}
  struct census_round_journal j={0};
  enum census_round_result result=census_choose_proven_continuation(s,r,&j);
  if(result!=CENSUS_ROUNDS_SELECTED_PENDING_PROOF || j.n!=4 || s->errors_n!=3 ||
      r->resolutions_n!=3 || r->certificates_n!=3 || r->proofs_n!=6 ||
      r->certificates[0].witness.chain_n!=2 ||
      r->certificates[1].witness.chain_n!=3 ||
      r->certificates[2].witness.chain_n!=4 ||
      !census_certificates_history_valid(s,r) ||
      !census_final_resolutions_valid(s,r,&j)) {rc=86;goto cleanup;}
  printf("deep_chain=grandparent_parent_child_independent_certificates:3 exact_suffix:PASS root_live:PASS\n");
cleanup:
  census_resolution_free(&b,r);census_global_free(s);close(ev.procfd);
  if(b.used||b.fd_failed||b.fd_peak>32) rc=87;
done:
  if(!f.retired) {char ack=0;(void)xwrite(f.control,"x",1);(void)xread(f.ready,&ack,1);}
  close(f.control);close(f.ready);int status=0;
  if(waitpid(root,&status,0)!=root||!WIFEXITED(status)||WEXITSTATUS(status)) rc=88;
  return rc;
}
int main(int argc,char **argv) {
  if(argc==4) return child_role(argc,argv);
  if(argc!=1 && argc!=2) return 2;
  alarm(8);struct utsname kernel;if(uname(&kernel)) return 20;
  if(strncmp(kernel.release,"6.8.",4)) return 21;
  struct rlimit lim={32,32};if(setrlimit(RLIMIT_NOFILE,&lim)) return 22;
  int ready[2],ctl[2];if(pipe(ready)||pipe(ctl)) return 23;
  pid_t root=fork();if(root<0) return 24;
  if(!root) {close(ready[0]);close(ctl[1]);char a[32],b[32];
    snprintf(a,sizeof a,"%d",ready[1]);snprintf(b,sizeof b,"%d",ctl[0]);
    char *args[]={argv[0],"owned-root",a,b,NULL};
    char *env[]={"GC_SESSION_ID=private-family", "GC_CITY_PATH=/private-fixture",
      "GC_TEMPLATE=fixture.worker","GC_RUNTIME_EPOCH=1","GC_INSTANCE_TOKEN=private-value",NULL};
    execve(argv[0],args,env);_exit(25);
  }
  close(ready[1]);close(ctl[0]);struct family f={.control=ctl[1],.ready=ready[0]};
  pid_t ids[3]={0};int rc=0;
  if(!xread(f.ready,ids,sizeof ids)) {rc=26;goto done;}
  f.root=ids[0];f.parent=ids[1];f.child=ids[2];
  if(f.root!=root || !(getpid()<f.root && f.root<f.parent && f.parent<f.child)) {rc=27;goto done;}
  init_evidence();struct census_budget b={.limit=16777216};
  struct census_global_source *s=census_global_new(&b,&f,enumerate_family,capture_family);
  struct census_resolution_ledger *r=census_resolution_new(&b);
  if(!s||!r) {rc=28;goto cleanup;}
  struct census_round_journal j={0};
  enum census_round_result result=census_choose_proven_continuation(s,r,&j);
  struct census_history_row *pa=census_history_find(&s->history->scans[0],f.parent);
  struct census_history_row *ch=census_history_find(&s->history->scans[0],f.child);
  struct census_history_row *r0=census_history_find(&s->history->scans[0],f.root);
  struct census_history_row *rt=s->history->n>1?census_history_find(&s->history->scans[1],f.root):NULL;
  fprintf(stderr,"diag scans=%u result=%d errors=%u certs=%u resolutions=%u proofs=%u pa=%d ch=%d rt=%d rootflag=%d ppidpa=%u ppidch=%u parentpid=%d childpid=%d parentcert=%d\n",
      j.n,result,s->errors_n,r->certificates_n,r->resolutions_n,r->proofs_n,
      census_row_verified(pa),census_row_verified(ch),census_row_verified(rt),
      rt?rt->identity.declared_root:0,pa?pa->identity.ppid:0,ch?ch->identity.ppid:0,
      f.parent,f.child,r->certificates_n?census_certificate_history(s->history,&r->certificates[0]):0);
  if(!pa||!ch||!r0||!rt||!census_row_verified(pa)||!census_row_verified(ch)||
     !census_row_verified(r0)||!census_same_owned(&r0->identity,&rt->identity)||
     !census_row_verified(rt)||!rt->identity.declared_root||
     pa->classification!=CENSUS_CLASS_MANAGED||ch->classification!=CENSUS_CLASS_MANAGED||
     !census_same_inherited(&pa->identity,&ch->identity)||
     !census_same_inherited(&pa->identity,&rt->identity)||
     pa->identity.ppid!=(uint32_t)f.root||ch->identity.ppid!=(uint32_t)f.parent||
     result!=CENSUS_ROUNDS_SELECTED_PENDING_PROOF||j.n!=4||s->errors_n!=2||
     j.selected_first!=2||j.selected_second!=3||j.selected_seal!=4||
     r->certificates_n!=2||r->resolutions_n!=2||r->proofs_n!=4||
     r->certificates[0].witness.retirement.pid!=(uint32_t)f.parent||
     r->proofs[0].source.kind!=CENSUS_ENUMERATED_ABSENT||
     r->proofs[0].source.pid!=(uint32_t)f.parent||
     strcmp(r->proofs[0].source.method,"pidfd_no_pid")||
     r->proofs[1].source.kind!=CENSUS_INCARNATION_RETIRED||
     r->proofs[1].source.start!=pa->identity.start||
     !r->proofs[1].protected_identity||r->proofs[1].certificate_id!=1||
     r->resolutions[0].error_index!=1||r->resolutions[0].proof_index!=1||
     r->certificates[1].witness.retirement.pid!=(uint32_t)f.child||
     r->certificates[1].witness.chain_n!=3||
     !r->proofs[3].protected_identity||r->proofs[3].certificate_id!=2||
     r->resolutions[1].error_index!=2||r->resolutions[1].proof_index!=3||
     !census_certificates_history_valid(s,r)||!census_final_resolutions_valid(s,r,&j)||
     !census_certificate_history(s->history,&r->certificates[0])) {rc=29;goto cleanup;}
  for(unsigned i=0;i<2;i++) if(strcmp(s->errors[i].raw.operation,"pidfd_open")||
      s->errors[i].raw.error!=ESRCH||s->errors[i].raw.start) {rc=30;goto cleanup;}
  struct census_history_row *failed=census_history_find(&s->history->scans[1],f.child);
  if(!failed||failed->failure!=CENSUS_ROW_CAPTURE_FAILED||
      strcmp(failed->raw_fault.operation,"pidfd_open")) {rc=31;goto cleanup;}
  /* Two separate real kernel no-PID witnesses; child uses parent's verified
   * certificate only as terminal chain continuity, never to cover its fault. */
  errno=0;int child_fd=(int)syscall(SYS_pidfd_open,f.child,0);int child_errno=errno;
  if(child_fd>=0) close(child_fd);
  if(child_fd!=-1||child_errno!=ESRCH||expired()) {rc=34;goto cleanup;}
  char ownns[128],rootns[128],rootpath[128];
  snprintf(rootpath,sizeof rootpath,"/proc/%d/ns/pid",f.root);
  ssize_t ownlen=readlink("/proc/self/ns/pid",ownns,sizeof ownns-1);
  ssize_t rootlen=readlink(rootpath,rootns,sizeof rootns-1);
  if(ownlen<=0||rootlen<=0||ownlen!=rootlen||memcmp(ownns,rootns,(size_t)ownlen)) {rc=35;goto cleanup;}
  printf("GREEN candidate-from=42b33 kernel=%s uid=%u elapsed_ms=%" PRIu64 " scans=%u result=SELECTED_PENDING_PROOF raw_errors=%u resolved=%u certs=%u proofs=%u parent=%d/%" PRIu64 " child=%d/%" PRIu64 " root=%d/%" PRIu64 " fd_peak=%u budget_peak=%zu\n",
      kernel.release,(unsigned)geteuid(),mono_ms()-(uint64_t)ev.monotonic_start.tv_sec*1000u-(uint64_t)ev.monotonic_start.tv_nsec/1000000u,
      j.n,s->errors_n,r->resolutions_n,r->certificates_n,r->proofs_n,f.parent,pa->identity.start,
      f.child,ch->identity.start,f.root,rt->identity.start,b.fd_peak,b.peak);
  printf("controls=managed_pair_same_tuple:1 live_root:1 namespace_equal:1 caller_excluded:1 child_independent_pidfd_open_errno:%d linked_certificate_valid:1 final_resolution_valid:1 provider_BOTH:unavailable_in_private_fixture\n",child_errno);
  for(unsigned i=0;i<s->errors_n;i++) printf("raw[%u]=pid:%u op:%s errno:%d start:%" PRIu64 "\n",i+1,
      s->errors[i].raw.pid,s->errors[i].raw.operation,s->errors[i].raw.error,s->errors[i].raw.start);
  if(argc==2) {
    struct census_json *json=census_json_new(&b);if(!json) {rc=36;goto cleanup;}
    census_json_text(json,"{\"schema\":\"source-proc-census-vector/v1\",\"census\":");
    census_json_census(json,s,r,&j);census_json_text(json,",\"errors\":");
    census_json_errors(json,s);
    uint64_t start=(uint64_t)ev.monotonic_start.tv_sec*1000u+(uint64_t)ev.monotonic_start.tv_nsec/1000000u;
    census_json_printf(json,",\"errors_total\":%u,\"errors_truncated\":false,\"duration_ms\":%" PRIu64 ",\"controller_pid\":%u,\"certificate_disposition\":\"provisional\"}",
        s->errors_total,mono_ms()-start,ev.binding.pid);
    if(!census_json_finalize(json)) {census_json_free(json);rc=37;goto cleanup;}
    int fd=open(argv[1],O_CREAT|O_EXCL|O_WRONLY|O_CLOEXEC,0600);
    if(fd<0) {census_json_free(json);rc=38;goto cleanup;}
    size_t done=0;while(done<json->n) {ssize_t n=write(fd,json->data+done,json->n-done);
      if(n<=0) {close(fd);census_json_free(json);rc=39;goto cleanup;}done+=(size_t)n;}
    close(fd);census_json_free(json);
  }
  /* Source-level refusal controls: no mutation is serialized or admitted. */
  uint64_t oldstart=r->certificates[1].witness.chain[1].start;
  r->certificates[1].witness.chain[1].start++;
  bool wrong_suffix=census_certificates_history_valid(s,r);
  r->certificates[1].witness.chain[1].start=oldstart;
  uint32_t olduid=r->certificates[1].witness.chain[1].uids[0];
  r->certificates[1].witness.chain[1].uids[0]++;
  bool wrong_uid=census_certificates_history_valid(s,r);
  r->certificates[1].witness.chain[1].uids[0]=olduid;
  unsigned oldprior=r->certificates[0].prior_scan;
  r->certificates[0].prior_scan=0;
  bool bad_prior=census_certificates_history_valid(s,r);
  r->certificates[0].prior_scan=oldprior;
  unsigned oldindex=r->resolutions[1].error_index;
  r->resolutions[1].error_index=r->resolutions[0].error_index;
  bool duplicate_error=census_certificates_history_valid(s,r);
  r->resolutions[1].error_index=oldindex;
  struct census_history_row *late_root=census_history_find(&s->history->scans[3],f.root);
  if(!late_root) {rc=40;goto cleanup;}
  uint64_t oldroot=late_root->identity.start;late_root->identity.start++;
  bool root_changed=census_certificates_history_valid(s,r);
  late_root->identity.start=oldroot;
  int oldscan=r->proofs[1].source.scan_index;r->proofs[1].source.scan_index++;
  bool cross_scan=census_certificates_history_valid(s,r);
  r->proofs[1].source.scan_index=oldscan;
  struct census_history_scan *later=&s->history->scans[2];
  struct census_history_row *original_rows=later->rows;
  size_t original_n=later->allocated_rows;
  if(original_n!=2 || !(original_rows[1].pid<(uint32_t)f.parent)) {rc=42;goto cleanup;}
  struct census_history_row reappeared[3]={original_rows[0],original_rows[1],*pa};
  later->rows=reappeared;later->allocated_rows=3;
  bool same_incarnation_reappeared=census_certificates_history_valid(s,r);
  later->rows=original_rows;later->allocated_rows=original_n;
  struct timespec original_clock=ev.monotonic_start;
  ev.monotonic_start.tv_sec-=11;
  bool deadline_expired=census_certificates_history_valid(s,r);
  ev.monotonic_start=original_clock;
  if(wrong_suffix||wrong_uid||bad_prior||duplicate_error||root_changed||cross_scan||
      same_incarnation_reappeared||deadline_expired||
      !census_final_resolutions_valid(s,r,&j)) {rc=41;goto cleanup;}
  printf("negative=wrong_suffix:deny wrong_uid:deny bad_prior:deny duplicate_error:deny root_changed:deny cross_scan:deny same_incarnation_reappeared:deny expired_deadline:deny\n");
cleanup:
  census_resolution_free(&b,r);census_global_free(s);close(ev.procfd);
  if(b.used||b.fd_failed||b.fd_peak>32) rc=32;
done:
  if(!f.retired && f.control>=0) {char ack=0;(void)xwrite(f.control,"x",1);(void)xread(f.ready,&ack,1);}
  close(f.control);close(f.ready);int status=0;
  if(waitpid(root,&status,0)!=root||!WIFEXITED(status)||WEXITSTATUS(status)) rc=33;
  if(!rc) rc=reverse_control(argv[0]);
  if(!rc) rc=unequal_prior_control(argv[0]);
  if(!rc) rc=deep_chain_control(argv[0]);
  return rc;
}
