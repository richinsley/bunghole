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
set(OUTPUT_BIN "${CMAKE_BINARY_DIR}/bunghole")
set(OUTPUT_VM_AUDIO_BIN "${CMAKE_BINARY_DIR}/bunghole-vm-audio")

if(CMAKE_SYSTEM_NAME STREQUAL "Darwin")
    option(BUNGHOLE_CODESIGN "Codesign bunghole after build" ON)
    set(BUNGHOLE_CODESIGN_IDENTITY "-" CACHE STRING "codesign identity")
    set(BUNGHOLE_ENTITLEMENTS "${CMAKE_SOURCE_DIR}/entitlements.plist" CACHE FILEPATH "Path to entitlements plist")

    find_program(INSTALL_NAME_TOOL_EXECUTABLE install_name_tool)
    if(NOT INSTALL_NAME_TOOL_EXECUTABLE)
        message(FATAL_ERROR "install_name_tool not found")
    endif()

    execute_process(
        COMMAND pkg-config --variable=libdir opus
        OUTPUT_VARIABLE OPUS_LIBDIR
        OUTPUT_STRIP_TRAILING_WHITESPACE
    )
    set(OPUS_DYLIB "${OPUS_LIBDIR}/libopus.0.dylib")
    if(NOT EXISTS "${OPUS_DYLIB}")
        message(FATAL_ERROR "libopus runtime not found at: ${OPUS_DYLIB}")
    endif()

    if(BUNGHOLE_CODESIGN)
        find_program(CODESIGN_EXECUTABLE codesign)
        if(NOT CODESIGN_EXECUTABLE)
            message(FATAL_ERROR "codesign not found, but BUNGHOLE_CODESIGN is ON")
        endif()
        if(NOT EXISTS "${BUNGHOLE_ENTITLEMENTS}")
            message(FATAL_ERROR "Entitlements file not found: ${BUNGHOLE_ENTITLEMENTS}")
        endif()
    endif()
endif()

if(CMAKE_SYSTEM_NAME STREQUAL "Darwin" AND BUNGHOLE_CODESIGN)
    add_custom_target(bunghole ALL
        COMMAND ${CMAKE_COMMAND} -E env CGO_ENABLED=1
            go build -tags "${GO_TAGS}" -o ${OUTPUT_BIN} ./cmd/bunghole
        COMMAND ${CODESIGN_EXECUTABLE} --force --sign "${BUNGHOLE_CODESIGN_IDENTITY}"
            --entitlements "${BUNGHOLE_ENTITLEMENTS}" ${OUTPUT_BIN}
        WORKING_DIRECTORY ${CMAKE_SOURCE_DIR}
        COMMENT "Building and codesigning bunghole"
    )
else()
    add_custom_target(bunghole ALL
        COMMAND ${CMAKE_COMMAND} -E env CGO_ENABLED=1
            go build -tags "${GO_TAGS}" -o ${OUTPUT_BIN} ./cmd/bunghole
        WORKING_DIRECTORY ${CMAKE_SOURCE_DIR}
        COMMENT "Building bunghole"
    )
endif()

if(CMAKE_SYSTEM_NAME STREQUAL "Darwin")
    add_custom_target(bunghole-vm-audio ALL
        COMMAND ${CMAKE_COMMAND} -E env CGO_ENABLED=1
            go build -tags "${GO_TAGS}" -o ${OUTPUT_VM_AUDIO_BIN} ./cmd/bunghole-vm-audio
        COMMAND ${CMAKE_SOURCE_DIR}/scripts/rebase-opus.sh ${OUTPUT_VM_AUDIO_BIN} ${CMAKE_BINARY_DIR}/libopus.0.dylib ${INSTALL_NAME_TOOL_EXECUTABLE}
        WORKING_DIRECTORY ${CMAKE_SOURCE_DIR}
        COMMENT "Building bunghole-vm-audio and rebasing libopus to @loader_path"
    )
endif()
