#define _GNU_SOURCE
#include "census-owned-identity.h"
#include <assert.h>
#include <stdio.h>
int main(void) {
  uint32_t u[4];
  assert(census_parse_uids("Uid:\t1000\t1001\t1002\t1003\n",u));
  assert(u[0]==1000 && u[1]==1001 && u[2]==1002 && u[3]==1003);
  const char *bad[]={"Uid: 1 2 3","Uid: 1 2 3 4 5","Uid: -1 2 3 4",
    "Uid: 4294967296 2 3 4","Uid: 1x 2 3 4","Name: 1 2 3 4",NULL};
  for(size_t i=0;i<6;i++) assert(!census_parse_uids(bad[i],u));
  assert(!census_parse_uids(NULL,u));
  struct census_budget budget={.limit=16777216};
  struct census_owned_identity in={.pid=30,.ppid=20,.pgid=30,.start=300,.epoch=1,
    .sid="S",.city="/city",.template="T",.name="child"};
  struct census_owned_identity *out=census_alloc(&budget,sizeof *out);
  assert(out && budget.used==sizeof *out);
  assert(census_identity_copy(&budget,out,&in));
  assert(out->sid!=in.sid && !strcmp(out->sid,in.sid));
  size_t owned=budget.used;
  in.sid="changed";assert(!strcmp(out->sid,"S"));
  census_identity_clear(&budget,out);census_release(&budget,out,sizeof *out);
  assert(budget.used==0 && owned>sizeof *out);
  budget.used=budget.limit-1;assert(!census_alloc(&budget,2));
  assert(!census_alloc(&budget,SIZE_MAX));
  struct census_owned_identity failed={0};size_t before=budget.used;
  assert(!census_identity_copy(&budget,&failed,&in));assert(budget.used==before);
  puts("PASS owned certificate strings/container accounting + strict real-UID line parser; no proc/RPC");
}
