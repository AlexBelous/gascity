/* Owned disposable filesystem tree; no host /proc enumeration. */
#define GC_HELPER_TEST
#define main observer_fixture_main
#include "observer.c"
#undef main
#include "census-v3-enumerate.h"
#include <assert.h>
#include <sys/resource.h>
static unsigned getdents_calls;
static int partial_then_fail(int fd,void *buf,size_t n) {
  if(++getdents_calls==1) return (int)syscall(SYS_getdents64,fd,buf,n);
  ev.monotonic_start.tv_sec-=10;errno=EACCES;return -1;
}
int main(void) {
  char directory[]="/tmp/native27-owned-pid-enumeration-XXXXXX";
  assert(mkdtemp(directory));int fd=open(directory,O_RDONLY|O_DIRECTORY|O_CLOEXEC);assert(fd>=0);
  assert(!mkdirat(fd,"20",0700));assert(!mkdirat(fd,"3",0700));assert(!mkdirat(fd,"self",0700));
  ev.procfd=fd;assert(!clock_gettime(CLOCK_MONOTONIC,&ev.monotonic_start));
  struct rlimit limit={32,32};assert(!setrlimit(RLIMIT_NOFILE,&limit));
  struct census_budget budget={.limit=16777216};struct census_pid_list list={0};
  uint32_t order[]={20,2,90,3,1,8,8};
  struct census_pid_list sort_fixture={.p=order,.n=7,.capacity=7};
  assert(census_pid_sort(&sort_fixture));
  const uint32_t expected[]={1,2,3,8,8,20,90};assert(!memcmp(order,expected,sizeof order));
  uint32_t uids[4];assert(census_parse_uids("Uid:\t1000\t1000\t1000\t1000",uids));
  struct census_owned_identity identity={0},input={.pid=20,.sid="",.city="",.template="",.name="owned enumeration fixture"};
  assert(census_identity_copy(&budget,&identity,&input));
  census_identity_clear(&budget,&identity);assert(!budget.used);
  assert(census_pid_enumerate(&budget,&list));assert(list.n==2 && list.p[0]==3 && list.p[1]==20);
  assert(budget.used==list.capacity*sizeof *list.p);
  census_pid_clear(&budget,&list);assert(!budget.used);
  budget.limit=1;assert(!census_pid_enumerate(&budget,&list));census_pid_clear(&budget,&list);assert(!budget.used);
  budget.limit=16777216;assert(!mkdirat(fd,"2147483648",0700));
  assert(!census_pid_enumerate(&budget,&list));census_pid_clear(&budget,&list);assert(!budget.used);
  assert(!unlinkat(fd,"2147483648",AT_REMOVEDIR));
  census_test_getdents=partial_then_fail;
  assert(!census_pid_enumerate(&budget,&list) && list.n==2 &&
    list.first_error==EACCES && !strcmp(list.first_operation,"getdents64") &&
    list.later_error==ETIMEDOUT && !strcmp(list.later_operation,"sort_deadline") && errno==EACCES);
  census_test_getdents=NULL;census_pid_clear(&budget,&list);assert(!budget.used);
  assert(!clock_gettime(CLOCK_MONOTONIC,&ev.monotonic_start));
  ev.monotonic_start.tv_sec-=10;assert(!census_pid_enumerate(&budget,&list));census_pid_clear(&budget,&list);
  assert(!unlinkat(fd,"20",AT_REMOVEDIR));assert(!unlinkat(fd,"3",AT_REMOVEDIR));assert(!unlinkat(fd,"self",AT_REMOVEDIR));
  close(fd);assert(!rmdir(directory));
  puts("PASS actual numeric enumeration on OWNED fixture tree, sorted PIDs/charged arrays/FD32; memory/integer/deadline failures DENY; no host census");
}
