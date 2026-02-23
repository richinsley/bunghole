cmake_minimum_required(VERSION 3.20)
project(bunghole)

find_package(PkgConfig REQUIRED)

if(CMAKE_SYSTEM_NAME STREQUAL "Linux")
    pkg_check_modules(X11 REQUIRED x11 xext xfixes xtst)
    pkg_check_modules(FFMPEG REQUIRED libavcodec libavutil libswscale)
elseif(CMAKE_SYSTEM_NAME STREQUAL "Darwin")
    pkg_check_modules(FFMPEG REQUIRED libavcodec libavutil libswscale)
endif()

set(GO_TAGS "nolibopusfile")

add_custom_target(bunghole ALL
    COMMAND ${CMAKE_COMMAND} -E env CGO_ENABLED=1
        go build -tags "${GO_TAGS}" -o ${CMAKE_BINARY_DIR}/bunghole ./cmd/bunghole
    WORKING_DIRECTORY ${CMAKE_SOURCE_DIR}
    COMMENT "Building bunghole"
)
