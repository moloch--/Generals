# GeneralsX @build BenderAI 21/04/2026 libcurl integration for the updater and verified Online TLS.
# Finds libcurl via vcpkg on modern Linux, macOS, and Windows builds.

if(SAGE_UPDATE_CHECK OR SAGE_ONLINE_TLS)
    find_package(CURL CONFIG REQUIRED)
    message(STATUS "libcurl found: ${CURL_VERSION_STRING}")
endif()
