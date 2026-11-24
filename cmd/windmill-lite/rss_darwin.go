package main

/*
#include <mach/mach.h>

// wl_resident_size is the task's current resident set size, in bytes, or
// -1 when task_info fails.
static long long wl_resident_size(void) {
	mach_task_basic_info_data_t info;
	mach_msg_type_number_t count = MACH_TASK_BASIC_INFO_COUNT;
	if (task_info(mach_task_self(), MACH_TASK_BASIC_INFO, (task_info_t)&info, &count) != KERN_SUCCESS) {
		return -1;
	}
	return (long long)info.resident_size;
}
*/
import "C"

import "errors"

// currentRSS is the engine's current resident set size, in bytes:
// task_info's MACH_TASK_BASIC_INFO resident_size, not its peak
// (resident_size_max).
func currentRSS() (int64, error) {
	v := C.wl_resident_size()
	if v < 0 {
		return 0, errors.New("task_info: MACH_TASK_BASIC_INFO failed")
	}
	return int64(v), nil
}
