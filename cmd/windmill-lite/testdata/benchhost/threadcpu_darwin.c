// threadcpu PID — each thread's CPU time of a process on macOS, the
// counterpart of Linux's /proc/<pid>/task/*/stat for BenchmarkEmbedder
// (bench_threads_test.go): one line per thread, "id user_ns system_ns
// name", in the order the kernel lists them (the main thread first). The id
// is the 64-bit thread id that /usr/bin/sample prints as Thread_<id>. A
// thread without a name gets the process's name if it is the first, else
// "thread". It reads a process of the same user without privileges.
#include <libproc.h>
#include <stdio.h>
#include <stdlib.h>
#include <sys/proc_info.h>

// The thread ids of a process (xnu's PROC_PIDLISTTHREADIDS, which the
// public SDK header leaves out).
#ifndef PROC_PIDLISTTHREADIDS
#define PROC_PIDLISTTHREADIDS 28
#endif

int main(int argc, char **argv) {
	if (argc != 2) {
		fprintf(stderr, "usage: threadcpu PID\n");
		return 2;
	}
	int pid = atoi(argv[1]);
	uint64_t ids[4096];
	int n = proc_pidinfo(pid, PROC_PIDLISTTHREADIDS, 0, ids, sizeof ids);
	if (n <= 0) {
		perror("proc_pidinfo");
		return 1;
	}
	n /= sizeof ids[0];
	char pname[PROC_PIDPATHINFO_MAXSIZE] = "";
	proc_name(pid, pname, sizeof pname);
	for (int i = 0; i < n; i++) {
		struct proc_threadinfo ti;
		if (proc_pidinfo(pid, PROC_PIDTHREADID64INFO, ids[i], &ti, sizeof ti) != sizeof ti) {
			continue;
		}
		const char *name = ti.pth_name[0] ? ti.pth_name : (i == 0 && pname[0] ? pname : "thread");
		printf("%llu %llu %llu %s\n", (unsigned long long)ids[i], (unsigned long long)ti.pth_user_time,
		       (unsigned long long)ti.pth_system_time, name);
	}
	return 0;
}
