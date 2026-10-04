CC ?= cc
SOURCE_REVISION ?=
CFLAGS ?= -O2 -std=c11 -Wall -Wextra -Werror -fstack-protector-strong -D_FORTIFY_SOURCE=2
LDFLAGS ?= -static -Wl,-z,relro,-z,now,-z,noexecstack
OUT ?= /tmp/gc-process-observer-helper

.PHONY: build fixture test
build:
	@test "$$(printf '%s' '$(SOURCE_REVISION)' | wc -c | tr -d ' ')" = 40
	$(CC) $(CFLAGS) -DHELPER_SOURCE_REVISION='"$(SOURCE_REVISION)"' observer.c $(LDFLAGS) -o $(OUT)
fixture:
	$(CC) $(CFLAGS) -DGC_HELPER_TEST -DHELPER_SOURCE_REVISION='"$(SOURCE_REVISION)"' observer.c $(LDFLAGS) -o $(OUT)-fixture
	$(CC) $(CFLAGS) -DGC_HELPER_TEST -DTEST_MAX_PIDS=4 -DHELPER_SOURCE_REVISION='"$(SOURCE_REVISION)"' observer.c $(LDFLAGS) -o $(OUT)-limit-fixture
test: fixture
	python3 selftest.py $(OUT)-fixture $(OUT)-limit-fixture
