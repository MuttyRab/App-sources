/* Compatibility shim: mingw-w64 v11 headers lack the Windows 10 1709+ thread power throttling API types. */
#ifndef LOCALCHAT_MINGW_COMPAT_H
#define LOCALCHAT_MINGW_COMPAT_H
#if defined(_WIN32) && !defined(THREAD_POWER_THROTTLING_CURRENT_VERSION)
typedef struct _THREAD_POWER_THROTTLING_STATE {
    unsigned long Version;
    unsigned long ControlMask;
    unsigned long StateMask;
} THREAD_POWER_THROTTLING_STATE;
#define THREAD_POWER_THROTTLING_CURRENT_VERSION 1
#define THREAD_POWER_THROTTLING_EXECUTION_SPEED 0x1
#endif
#endif
