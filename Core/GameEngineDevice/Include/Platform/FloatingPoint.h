/*
** Command & Conquer Generals Zero Hour(tm)
** Copyright 2025 Electronic Arts Inc.
**
** This program is free software: you can redistribute it and/or modify
** it under the terms of the GNU General Public License as published by
** the Free Software Foundation, either version 3 of the License, or
** (at your option) any later version.
*/

#pragma once

#if defined(_WIN32) && !defined(__MINGW32__)
#include <float.h>
#else
#include <fenv.h>
#if defined(__SSE__) || defined(__x86_64__)
#include <xmmintrin.h>
#endif
#endif

// GeneralsX @refactor Codex 10/10/2026 Share the production FPU reset with deterministic math tests.
// GeneralsX @bugfix Codex 10/10/2026 Preserve double precision in deterministic x87 return expressions.
// MSVC x86 can emit x87 arithmetic at a double return even when scalar arithmetic otherwise uses SSE2.
inline void ResetSimulationFloatingPointMode()
{
    // GeneralsX @bugfix BenderAI 14/03/2026 Align non-Windows x86 FPU control with the Windows replay determinism baseline.
    // GeneralsX @bugfix OpenAI 29/07/2026 Use the portable x87 path when strict-mode MinGW hides MSVC CRT controls.
#if defined(_WIN32) && !defined(__MINGW32__)
    _fpreset();

    unsigned int newVal = _statusfp();
    newVal = (newVal & ~_MCW_RC) | (_RC_NEAR & _MCW_RC);
#if defined(USE_DETERMINISTIC_MATH)
    newVal = (newVal & ~_MCW_PC) | (_PC_53 & _MCW_PC);
#else
    newVal = (newVal & ~_MCW_PC) | (_PC_24 & _MCW_PC);
#endif
    _controlfp(newVal, _MCW_PC | _MCW_RC);
#else
    fesetenv(FE_DFL_ENV);
    feclearexcept(FE_ALL_EXCEPT);
    fesetround(FE_TONEAREST);

#if defined(__i386__) || defined(__x86_64__)
    unsigned short controlWord = 0;
    __asm__ __volatile__("fnstcw %0" : "=m" (controlWord));
    controlWord = static_cast<unsigned short>(controlWord & ~0x0F00u);
#if defined(USE_DETERMINISTIC_MATH)
    controlWord = static_cast<unsigned short>(controlWord | 0x0200u);
#endif
    __asm__ __volatile__("fldcw %0" : : "m" (controlWord));
#endif

#if defined(__SSE__) || defined(__x86_64__)
    unsigned int mxcsr = static_cast<unsigned int>(_mm_getcsr());
    mxcsr = (mxcsr & ~static_cast<unsigned int>(_MM_ROUND_MASK)) | static_cast<unsigned int>(_MM_ROUND_NEAREST);
    _mm_setcsr(mxcsr);
#endif
#endif
}
