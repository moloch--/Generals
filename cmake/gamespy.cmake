set(GS_OPENSSL FALSE)
set(GAMESPY_SERVER_NAME "server.cnc-online.net")

FetchContent_Declare(
    gamespy
    GIT_REPOSITORY https://github.com/TheSuperHackers/GamespySDK.git
    GIT_TAG        07e3d15c500415abc281efb74322ab6d9c857eb8
)

FetchContent_MakeAvailable(gamespy)

# GeneralsX @bugfix Codex 02/08/2026 Keep legacy SDK caches out of verified modern SFX payloads.
# The replacement Online queues do not use GameSpy's disk-backed profile,
# transfer, or failed-stats storage. Modern legacy fallback builds keep their
# account and matchmaking paths but deliberately omit obsolete disk-backed
# caches, transfers, downloads, and failed-stat queues.
if(NOT IS_VS6_BUILD)
    target_compile_definitions(gsinterface INTERFACE NOFILE)
    target_compile_definitions(gamespy PUBLIC NOFILE)
endif()
