/* Key-only structural regression and owned same-UID process tree. No host
 * census, installed helper, capabilities, provider authority or RPC. */
#define GC_HELPER_TEST
#define main observer_fixture_main
#include "observer.c"
#undef main
#include "census-bounded-read.h"
#include "census-root-classification.h"
#include <assert.h>
#include <sys/resource.h>
#include <sys/wait.h>

static void write_environment(int dir,const char *data,size_t n) {
  int fd=openat(dir,"environ",O_WRONLY|O_CREAT|O_TRUNC|O_CLOEXEC,0600);assert(fd>=0);
  assert(write(fd,data,n)==(ssize_t)n);assert(!close(fd));
}
static void check_environment(int dir,const char *data,size_t n,bool parsed,
    enum census_row_class expected) {
  struct census_budget budget={.limit=16777216};struct census_owned_identity row={0};
  write_environment(dir,data,n);
  bool ok=census_read_bounded_environment(&budget,dir,&row);
  enum census_row_class classification=CENSUS_CLASS_INVALID;
  if(ok) {
    row.pid=2;row.ppid=1;row.pgid=2;row.start=1;row.name="structural-fixture";
    row.stat_revalidated=row.pidfd_bound=row.uids_revalidated=true;
    classification=census_classify_owned(&row);row.name=NULL;
  }
  census_identity_clear(&budget,&row);assert(!budget.used);
  assert(ok==parsed && classification==expected);
}
static void structural(void) {
  char path[]="/tmp/native27-ownership-env-XXXXXX";assert(mkdtemp(path));
  int dir=open(path,O_RDONLY|O_DIRECTORY|O_CLOEXEC);assert(dir>=0);
  const char config[]="GC_DOLT_HOST=fixture\0GC_DOLT_PORT=0\0GC_TELEMETRY=off\0";
  check_environment(dir,config,sizeof config-1,true,CENSUS_CLASS_NONMANAGED);
  const char near[]="GC_SESSION_ID_EXTRA=fixture\0GC_CITY_PATH_EXTRA=fixture\0";
  check_environment(dir,near,sizeof near-1,true,CENSUS_CLASS_NONMANAGED);
  const char *keys[]={"GC_SESSION_ID","GC_TEMPLATE","GC_RUNTIME_EPOCH","GC_INSTANCE_TOKEN"};
  for(size_t i=0;i<4;i++) {
    char input[128];size_t n=(size_t)snprintf(input,sizeof input,"GC_TELEMETRY=off");
    n++;n+=(size_t)snprintf(input+n,sizeof input-n,"%s=%s",keys[i],i==2?"1":"present");n++;
    check_environment(dir,input,n,true,CENSUS_CLASS_INVALID);
    n=(size_t)snprintf(input,sizeof input,"%s=",keys[i])+1;
    check_environment(dir,input,n,i!=2,CENSUS_CLASS_INVALID);
  }
  const char full[]="GC_SESSION_ID=fixture-sid\0GC_CITY_PATH=/fixture\0GC_TEMPLATE=fixture.worker\0GC_RUNTIME_EPOCH=1\0GC_INSTANCE_TOKEN=fixture-token\0GC_TELEMETRY=off\0";
  check_environment(dir,full,sizeof full-1,true,CENSUS_CLASS_MANAGED);
  const char fallback[]="GC_SESSION_ID=fixture-sid\0GC_CITY=/fixture\0GC_TEMPLATE=fixture.worker\0GC_RUNTIME_EPOCH=1\0GC_INSTANCE_TOKEN=fixture-token\0GC_DOLT_HOST=fixture\0";
  check_environment(dir,fallback,sizeof fallback-1,true,CENSUS_CLASS_MANAGED);
  const char duplicate[]="GC_SESSION_ID=x\0GC_SESSION_ID=y\0";
  check_environment(dir,duplicate,sizeof duplicate-1,false,CENSUS_CLASS_INVALID);
  const char malformed[]="GC_TELEMETRY\0";
  check_environment(dir,malformed,sizeof malformed-1,false,CENSUS_CLASS_INVALID);
  const char duplicate_config[]="GC_TELEMETRY=x\0GC_TELEMETRY=y\0";
  check_environment(dir,duplicate_config,sizeof duplicate_config-1,false,CENSUS_CLASS_INVALID);
  assert(!unlinkat(dir,"environ",0));assert(!close(dir));assert(!rmdir(path));
  puts("PASS structural: irrelevant config/near keys NONMANAGED; all four partial/empty ownership keys denied; full/fallback managed; duplicate/malformed guards retained");
}
static bool capture(void *context,uint32_t pid,struct census_budget *budget,
    struct census_owned_identity *out,struct census_capture_fault *fault) {
  (void)context;return census_capture_bounded_evidenced(pid,budget,out,fault);
}
static void city_context(int dir) {
  const char city[]="GC_CITY_PATH=/fixture\0GC_CITY=fixture-alias\0GC_TELEMETRY=off\0";
  check_environment(dir,city,sizeof city-1,true,CENSUS_CLASS_NONMANAGED);
  const char fallback[]="GC_CITY=fixture-alias\0";
  check_environment(dir,fallback,sizeof fallback-1,true,CENSUS_CLASS_NONMANAGED);
  const char *keys[]={"GC_SESSION_ID","GC_TEMPLATE","GC_RUNTIME_EPOCH","GC_INSTANCE_TOKEN"};
  for(size_t i=0;i<4;i++) {
    char input[160];size_t n=(size_t)snprintf(input,sizeof input,"GC_CITY_PATH=/fixture");n++;
    n+=(size_t)snprintf(input+n,sizeof input-n,"%s=",keys[i]);n++;
    check_environment(dir,input,n,i!=2,CENSUS_CLASS_INVALID);
  }
  const char duplicate[]="GC_CITY_PATH=/fixture\0GC_CITY_PATH=/other\0";
  check_environment(dir,duplicate,sizeof duplicate-1,false,CENSUS_CLASS_INVALID);
  const char invalid_utf8[]="GC_CITY_PATH=\xff\0";
  check_environment(dir,invalid_utf8,sizeof invalid_utf8-1,false,CENSUS_CLASS_INVALID);
  const char context_change[]="GC_CITY_PATH=/other\0GC_CITY=fixture-alias\0GC_TELEMETRY=off\0";
  struct census_budget budget={.limit=16777216};struct census_owned_identity a={0},b={0};
  write_environment(dir,city,sizeof city-1);assert(census_read_bounded_environment(&budget,dir,&a));
  write_environment(dir,context_change,sizeof context_change-1);assert(census_read_bounded_environment(&budget,dir,&b));
  a.name=b.name="fixture";
  assert(!strcmp(a.city,"/fixture") && !strcmp(b.city,"/other") && !census_equal_bounded(&a,&b));
  a.name=b.name=NULL;census_identity_clear(&budget,&a);census_identity_clear(&budget,&b);assert(!budget.used);
  puts("PASS city context: both city keys retained/nonmanaged without four session keys; empty ownership/duplicate/bad UTF8 DENY; effective city mutation preserved");
}
static void city_structural(void) {
  char path[]="/tmp/native27-city-context-XXXXXX";assert(mkdtemp(path));
  int dir=open(path,O_RDONLY|O_DIRECTORY|O_CLOEXEC);assert(dir>=0);city_context(dir);
  assert(!unlinkat(dir,"environ",0));assert(!close(dir));assert(!rmdir(path));
}
static void owned_tree(const char *self,bool city) {
  int ready[2],control[2];assert(!pipe(ready));assert(!pipe(control));
  pid_t parent=fork();assert(parent>=0);
  if(!parent) {
    close(ready[0]);close(control[1]);char a[32],b[32];
    snprintf(a,sizeof a,"%d",ready[1]);snprintf(b,sizeof b,"%d",control[0]);
    char *args[]={(char *)self,"config-parent",a,b,NULL};
    char *env[]={"GC_DOLT_HOST=fixture","GC_DOLT_PORT=0","GC_TELEMETRY=off",NULL};
    char *city_env[]={"GC_CITY_PATH=/fixture","GC_CITY=fixture-alias","GC_TELEMETRY=off",NULL};
    execve(self,args,city?city_env:env);_exit(2);
  }
  close(ready[1]);close(control[0]);pid_t leaf;
  assert(read(ready[0],&leaf,sizeof leaf)==sizeof leaf);
  uint32_t pids[2]={(uint32_t)parent,(uint32_t)leaf};
  if(pids[0]>pids[1]) {uint32_t swap=pids[0];pids[0]=pids[1];pids[1]=swap;}
  struct census_budget budget={.limit=16777216};
  struct census_history *history=census_history_new(&budget);assert(history);
  bool appended=census_history_append(history,&budget,pids,2,0,CENSUS_INITIAL,1,NULL,capture);
  bool roots=census_root_classify(history),parent_ok=false,leaf_ok=false;
  for(size_t i=0;i<2;i++) {
    struct census_history_row *r=&history->scans[0].rows[i];
    if(r->pid==(uint32_t)parent) parent_ok=r->classification==CENSUS_CLASS_NONMANAGED &&
      r->failure==CENSUS_ROW_OK && r->identity.uids[1]==geteuid() && r->identity.pidfd_bound &&
      (!city || !strcmp(r->identity.city,"/fixture"));
    else leaf_ok=r->classification==CENSUS_CLASS_MANAGED && r->failure==CENSUS_ROW_OK &&
      r->identity.declared_root && r->identity.ppid==(uint32_t)parent && r->identity.uids[1]==geteuid();
  }
  census_history_free(&budget,history);assert(!budget.used);
  /* Always reap our tree before reporting the expected old-source failure. */
  assert(write(control[1],"x",1)==1);assert(!close(control[1]));assert(!close(ready[0]));
  int status;assert(waitpid(parent,&status,0)==parent && WIFEXITED(status) && !WEXITSTATUS(status));
  assert(appended && roots && parent_ok && leaf_ok);
  puts("PASS owned process: actual flags0 pidfd/two-pass stat+UID+environment; config-only parent NONMANAGED; full managed child root derived from verified parent");
}
int main(int argc,char **argv) {
  if(argc==4 && !strcmp(argv[1],"managed-leaf")) {
    assert(write(atoi(argv[2]),"r",1)==1);close(atoi(argv[2]));
    char cmd;assert(read(atoi(argv[3]),&cmd,1)==1);close(atoi(argv[3]));return 0;
  }
  if(argc==4 && !strcmp(argv[1],"config-parent")) {
    int ready=atoi(argv[2]),control=atoi(argv[3]),leaf_ready[2],leaf_control[2];
    assert(!pipe(leaf_ready));assert(!pipe(leaf_control));pid_t leaf=fork();assert(leaf>=0);
    if(!leaf) {
      close(leaf_ready[0]);close(leaf_control[1]);close(ready);close(control);
      char a[32],b[32];snprintf(a,sizeof a,"%d",leaf_ready[1]);snprintf(b,sizeof b,"%d",leaf_control[0]);
      char *args[]={argv[0],"managed-leaf",a,b,NULL};
      char *env[]={"GC_SESSION_ID=fixture-sid","GC_CITY_PATH=/fixture","GC_TEMPLATE=fixture.worker",
        "GC_RUNTIME_EPOCH=1","GC_INSTANCE_TOKEN=fixture-token","GC_TELEMETRY=off",NULL};
      execve(argv[0],args,env);_exit(2);
    }
    close(leaf_ready[1]);close(leaf_control[0]);char cmd;
    assert(read(leaf_ready[0],&cmd,1)==1);close(leaf_ready[0]);
    assert(write(ready,&leaf,sizeof leaf)==sizeof leaf);close(ready);
    assert(read(control,&cmd,1)==1);close(control);
    assert(write(leaf_control[1],"x",1)==1);close(leaf_control[1]);int status;
    assert(waitpid(leaf,&status,0)==leaf && WIFEXITED(status) && !WEXITSTATUS(status));return 0;
  }
  struct rlimit fdlimit={32,32};assert(!setrlimit(RLIMIT_NOFILE,&fdlimit));
  assert(!clock_gettime(CLOCK_MONOTONIC,&ev.monotonic_start));
  ev.procfd=open("/proc",O_RDONLY|O_DIRECTORY|O_CLOEXEC);assert(ev.procfd>=0);
  ev.binding.pid=(uint32_t)getpid();ev.trusted_kernel=true;
  if(argc==2 && !strcmp(argv[1],"structural")) structural();
  else if(argc==2 && !strcmp(argv[1],"owned")) owned_tree(argv[0],false);
  else if(argc==2 && !strcmp(argv[1],"owned-city")) owned_tree(argv[0],true);
  else if(argc==2 && !strcmp(argv[1],"city")) city_structural();
  else return 2;
  assert(!close(ev.procfd));return 0;
}
