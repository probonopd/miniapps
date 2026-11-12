/* asound.h wants libc types; echocap has no libc. 32-bit ARM layout. */
typedef long time_t;
struct timespec { long tv_sec; long tv_nsec; };
struct timeval { long tv_sec; long tv_usec; };
