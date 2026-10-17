CC ?= cc
SOURCE_REVISION ?=
CFLAGS ?= -O2 -std=c11 -Wall -Wextra -Werror -fstack-protector-strong -D_FORTIFY_SOURCE=2
LDFLAGS ?= -static -Wl,-z,relro,-z,now,-z,noexecstack
OUT ?= /tmp/gc-process-observer-helper

.PHONY: build build-v3 fixture test proof-test kernel-test send-test v3-fatal-test original-full-poll-test original-full-poll-kernel-test
build:
	@test "$$(printf '%s' '$(SOURCE_REVISION)' | wc -c | tr -d ' ')" = 40
	$(CC) $(CFLAGS) -DHELPER_SOURCE_REVISION='"$(SOURCE_REVISION)"' observer.c $(LDFLAGS) -o $(OUT)
build-v3:
	@test "$$(printf '%s' '$(SOURCE_REVISION)' | wc -c | tr -d ' ')" = 40
	$(CC) $(CFLAGS) -DHELPER_SOURCE_REVISION='"$(SOURCE_REVISION)"' observer-v3.c $(LDFLAGS) -o $(OUT)
fixture:
	$(CC) $(CFLAGS) -DGC_HELPER_TEST -DHELPER_SOURCE_REVISION='"$(SOURCE_REVISION)"' observer.c $(LDFLAGS) -o $(OUT)-fixture
	$(CC) $(CFLAGS) -DGC_HELPER_TEST -DTEST_MAX_PIDS=4 -DHELPER_SOURCE_REVISION='"$(SOURCE_REVISION)"' observer.c $(LDFLAGS) -o $(OUT)-limit-fixture
test: fixture proof-test send-test v3-fatal-test original-full-poll-test
	python3 selftest.py $(OUT)-fixture $(OUT)-limit-fixture

proof-test:
	$(CC) $(CFLAGS) census-proof-test.c -o $(OUT)-proof-test
	$(OUT)-proof-test
kernel-test:
	$(CC) $(CFLAGS) -DHELPER_SOURCE_REVISION='"$(SOURCE_REVISION)"' kernel-selftest.c $(LDFLAGS) -o $(OUT)-kernel-test
	python3 -c 'import subprocess; subprocess.run(["$(OUT)-kernel-test"], check=True, timeout=20)'

# Portable owned sockets exercise the actual sender without invoking a helper.
send-test:
	python3 test_send_output_budget.py

v3-fatal-test:
	python3 test_fatal_stages.py

original-full-poll-test:
	python3 test_original_full_poll_companion.py

# Real owned-child witness; no host enumeration or helper/RPC invocation.
original-full-poll-kernel-test:
	$(CC) $(CFLAGS) census-original-full-poll-kernel-test.c -o $(OUT)-original-full-poll-test
	$(OUT)-original-full-poll-test positive > $(OUT)-original-full-poll-vector.json
	@set -eu; for mode in no-stage2 wrong-uid wrong-start live-companion-fd reaped-original; do $(OUT)-original-full-poll-test $$mode; done
