# GeneralsX @feature fbraz 03/05/2026
# Deterministic cross-platform math library integration (Phase 4)
# GameMath: fdlibm-based deterministic math functions for bit-exact replay validation
#
# Strategy:
# - Enable deterministic math via FetchContent (not on VC6, which uses x87 asm)
# - Define USE_DETERMINISTIC_MATH compile flag when enabled
# - Wrappers in wwmath.h conditionally dispatch to GameMath (deterministic) or CRT (fast fallback)
#
# Reference: Okladnoj et al., PR #2670, TheSuperHackers/GeneralsGameCode
# https://github.com/TheSuperHackers/GeneralsGameCode/pull/2670
#
# Upstream reference: fdlibm (Berkeley math library) provides platform-independent
# implementations of standard math functions (sin, cos, sqrt, atan2, etc.) that produce
# identical results on all architectures when compiled with the same precision flags.

# Enable deterministic math only for non-VC6 builds (VC6 uses native x87 asm)
if(NOT IS_VS6_BUILD)
    option(SAGE_USE_DETERMINISTIC_MATH "Use fdlibm-based deterministic math for cross-platform replay validation" ON)
else()
    # VC6 uses native x87 inline asm; deterministic mode not applicable
    set(SAGE_USE_DETERMINISTIC_MATH OFF)
endif()

if(SAGE_USE_DETERMINISTIC_MATH)
    message(STATUS "Configuring GameMath (fdlibm-based deterministic math)...")

    include(FetchContent)

    # FORCE is required to guarantee cross-platform bit-exact determinism.
    # Intrinsics can select platform-specific SIMD implementations.
    set(GM_ENABLE_INTRINSICS OFF CACHE BOOL "Disable intrinsics for cross-arch determinism" FORCE)
    set(GM_ENABLE_TESTS OFF CACHE BOOL "Disable GameMath tests" FORCE)
    set(gamemath_SHARED_LIBS OFF CACHE BOOL "Force GameMath as a static library" FORCE)

    FetchContent_Declare(
        gamemath
        GIT_REPOSITORY https://github.com/TheSuperHackers/GameMath.git
        GIT_TAG        59f7ccd494f7e7c916a784ac26ef266f9f09d78d
    )

    # Make GameMath available (FetchContent_MakeAvailable is idempotent)
    FetchContent_MakeAvailable(gamemath)

    # WWMath is included transitively by many engine targets. Give every target the
    # same header view so inline wrappers cannot violate the one-definition rule.
    include_directories(${gamemath_SOURCE_DIR}/include)
    add_compile_definitions(USE_DETERMINISTIC_MATH=1)

    message(STATUS "GameMath deterministic math enabled (fdlibm backend)")
    message(STATUS "  Math operations will be bit-exact across platforms")
    message(STATUS "  Performance: Slightly slower than CRT but guarantees replay determinism")

else()
    message(STATUS "Deterministic math disabled (SAGE_USE_DETERMINISTIC_MATH=OFF)")
    message(STATUS "  Math operations will use platform-native CRT/x87")
    message(STATUS "  Note: Replays may differ between platforms due to FMA/rounding differences")
endif()
