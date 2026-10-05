/* Disposable file tree: buffer/parser tests, not host process proofs. */
#define GC_HELPER_TEST
#define main observer_fixture_main
#include "observer.c"
#undef main
#include "census-bounded-read.h"
#include <assert.h>
static void write_owned(int dir,const char *name,const void *data,size_t n) {
  int fd=openat(dir,name,O_WRONLY|O_CREAT|O_TRUNC|O_CLOEXEC,0600);assert(fd>=0);
  assert(write(fd,data,n)==(ssize_t)n);assert(!close(fd));
}
int main(void) {
  char path[]="/tmp/native27-bounded-read-XXXXXX";assert(mkdtemp(path));
  int dir=open(path,O_RDONLY|O_DIRECTORY|O_CLOEXEC);assert(dir>=0);
  assert(!clock_gettime(CLOCK_MONOTONIC,&ev.monotonic_start));
  struct census_budget budget={.limit=16777216};struct census_buffer b={0};
  ev.procfd=open("/proc",O_RDONLY|O_DIRECTORY|O_CLOEXEC);assert(ev.procfd>=0);
  assert(census_fd_initialize(&budget) && budget.fd_tracking && budget.fd_peak<=32 && !budget.used);
  unsigned baseline=budget.fd_live;
  write_owned(dir,"data","abcd",4);
  assert(census_buffer_read(&budget,dir,"data",4,&b) && b.n==4 && budget.used==b.capacity);
  census_buffer_clear(&budget,&b);assert(!budget.used);
  assert(budget.fd_live==baseline && !budget.fd_failed);
  assert(!census_buffer_read(&budget,dir,"data",3,&b) && errno==EFBIG && !budget.used);
  unsigned char large[5000];memset(large,'x',sizeof large);write_owned(dir,"data",large,sizeof large);
  budget.limit=10000;
  /* Growth must charge BOTH4096+8192, not merely replace accounting. */
  assert(!census_buffer_read(&budget,dir,"data",8192,&b) && errno==ENOMEM && !budget.used);
  budget.limit=16777216;assert(census_buffer_read(&budget,dir,"data",8192,&b));
  assert(b.n==5000 && budget.peak>=12288 && budget.peak<=budget.limit);
  census_buffer_clear(&budget,&b);assert(!budget.used);
  const char env[]="GC_SESSION_ID=s\0GC_CITY_PATH=/city\0GC_TEMPLATE=worker\0GC_RUNTIME_EPOCH=1\0GC_INSTANCE_TOKEN=private\0";
  write_owned(dir,"environ",env,sizeof env-1);struct census_owned_identity row={0};
  assert(census_read_bounded_environment(&budget,dir,&row));
  assert(!strcmp(row.sid,"s") && !strcmp(row.city,"/city") && row.epoch==1 &&
    row.environment_revalidated && !row.no_gc_environment && strlen(row.token)==64);
  census_identity_clear(&budget,&row);assert(!budget.used);memset(&row,0,sizeof row);
  const char duplicate[]="GC_SESSION_ID=s\0GC_SESSION_ID=other\0";
  write_owned(dir,"environ",duplicate,sizeof duplicate-1);
  assert(!census_read_bounded_environment(&budget,dir,&row));
  census_identity_clear(&budget,&row);assert(!budget.used);memset(&row,0,sizeof row);
  const char no_gc[]="process title without equals\0";
  write_owned(dir,"environ",no_gc,sizeof no_gc-1);
  assert(census_read_bounded_environment(&budget,dir,&row) && row.no_gc_environment);
  census_identity_clear(&budget,&row);assert(!budget.used);
  write_owned(dir,"status","Uid:\t1000\t1000\t1000\t1000\n",25);
  uint32_t uids[4];assert(census_read_bounded_uids(&budget,dir,uids) && uids[3]==1000);
  const char bad_uid[]="Uid:\t1\t2\t3\t4294967296\n";
  write_owned(dir,"status",bad_uid,sizeof bad_uid-1);
  assert(!census_read_bounded_uids(&budget,dir,uids) && !budget.used);
  char stat[512];size_t written=(size_t)snprintf(stat,sizeof stat,"2 (kernel worker) S");
  for(unsigned i=4;i<=22;i++) written+=(size_t)snprintf(stat+written,sizeof stat-written,
    " %u",i==9?PF_KTHREAD:0);
  write_owned(dir,"stat",stat,written);struct census_owned_identity kernel={0};
  assert(census_read_bounded_stat(&budget,dir,2,&kernel) && !kernel.start && !kernel.pgid && kernel.kernel_flags==PF_KTHREAD);
  written=(size_t)snprintf(stat,sizeof stat,"2 (user worker) S");
  for(unsigned i=4;i<=22;i++) written+=(size_t)snprintf(stat+written,sizeof stat-written," 0");
  write_owned(dir,"stat",stat,written);
  assert(!census_read_bounded_stat(&budget,dir,2,&kernel) && !budget.used);
  struct census_budget full={.limit=16777216,.fd_tracking=true,.fd_live=32,.fd_peak=32};
  assert(!census_buffer_read(&full,dir,"data",8192,&b) && full.fd_failed &&
    full.fd_peak==33 && full.fd_live==32 && !full.used);
  ev.monotonic_start.tv_sec-=10;
  assert(!census_buffer_read(&budget,dir,"data",8192,&b) && errno==ETIMEDOUT && !budget.used);
  assert(budget.fd_live==baseline && budget.fd_peak<=32 && !budget.fd_failed);
  assert(!unlinkat(dir,"data",0));assert(!unlinkat(dir,"environ",0));assert(!unlinkat(dir,"status",0));assert(!unlinkat(dir,"stat",0));
  assert(!close(dir));assert(!rmdir(path));
  assert(!close(ev.procfd));
  puts("PASS charged bounded buffers: growth peak/oversize/shared expired deadline/FDmeter overflow deny; own FD inventory; full env GC/noGC/duplicate keys and UID overflow; zero start/PGID only positive kernel flags; all budget released, no process authority");
}
