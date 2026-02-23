# Build stage — use the same Ubuntu as runtime so CGO links match
FROM ubuntu:24.04 AS build

ENV DEBIAN_FRONTEND=noninteractive

# Install Go
ADD https://go.dev/dl/go1.25.7.linux-amd64.tar.gz /tmp/go.tar.gz
RUN tar -C /usr/local -xzf /tmp/go.tar.gz && rm /tmp/go.tar.gz
ENV PATH="/usr/local/go/bin:${PATH}"

# Install C dev packages for CGO
RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates \
    gcc \
    libc6-dev \
    libx11-dev \
    libxext-dev \
    libxfixes-dev \
    libxtst-dev \
    libavcodec-dev \
    libavutil-dev \
    libswscale-dev \
    libpulse-dev \
    libopus-dev \
    pkg-config \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=1 go build -tags nolibopusfile -o /bunghole .

# Runtime stage
FROM ubuntu:24.04

ENV DEBIAN_FRONTEND=noninteractive

RUN apt-get update && apt-get install -y --no-install-recommends \
    # Xorg
    xserver-xorg-core \
    x11-utils \
    x11-xserver-utils \
    xauth \
    # GNOME desktop
    gnome-shell \
    gnome-terminal \
    gnome-control-center \
    nautilus \
    dbus-x11 \
    # Audio
    pipewire \
    wireplumber \
    pipewire-pulse \
    # FFmpeg runtime libs
    libavcodec60 \
    libavutil58 \
    libswscale7 \
    # X11 runtime libs
    libx11-6 \
    libxext6 \
    libxfixes3 \
    libxtst6 \
    # Opus
    libopus0 \
    # PulseAudio client lib (for go pulse bindings)
    libpulse0 \
    # Utils
    sudo \
    && rm -rf /var/lib/apt/lists/*

# Allow passwordless sudo for Xorg and kill (xserver_linux.go uses sudo)
RUN echo "ALL ALL=(ALL) NOPASSWD: /usr/bin/Xorg, /usr/bin/kill" > /etc/sudoers.d/bunghole \
    && chmod 0440 /etc/sudoers.d/bunghole

# Container Toolkit injects nvidia Xorg driver to /usr/lib/x86_64-linux-gnu/nvidia/xorg/
# but Xorg looks in /usr/lib/xorg/modules/ — symlink them into place
RUN ln -s /usr/lib/x86_64-linux-gnu/nvidia/xorg/nvidia_drv.so /usr/lib/xorg/modules/drivers/nvidia_drv.so \
    && ln -s /usr/lib/x86_64-linux-gnu/nvidia/xorg/libglxserver_nvidia.so /usr/lib/xorg/modules/extensions/libglxserver_nvidia.so

COPY --from=build /bunghole /usr/local/bin/bunghole

ENTRYPOINT ["bunghole", "--start-x"]
CMD ["--token", "changeme"]
