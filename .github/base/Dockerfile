# ==============================
# 第一阶段：编译 FFmpeg + libfdk_aac
# ==============================
FROM python:3.12-slim AS ffmpeg-builder

ENV TZ=Asia/Shanghai \
    DEBIAN_FRONTEND=noninteractive \
    FFMPEG_VERSION=6.1 \
    FDKAAC_VERSION=2.0.2

# 安装构建所需的工具和 -dev 依赖（包含 zlib 等）
RUN apt-get update && apt-get install -y --no-install-recommends \
    tzdata ca-certificates curl wget git build-essential pkg-config \
    yasm nasm libtool autoconf automake cmake \
    zlib1g-dev libssl-dev libpng-dev libjpeg-dev libfreetype6-dev \
    libopenjp2-7-dev libbz2-dev liblzma-dev libx264-dev \
    && ln -snf /usr/share/zoneinfo/$TZ /etc/localtime && echo $TZ > /etc/timezone && \
    rm -rf /var/lib/apt/lists/*

WORKDIR /tmp/build

# 编译 libfdk_aac
RUN wget -q https://github.com/mstorsjo/fdk-aac/archive/refs/tags/v${FDKAAC_VERSION}.tar.gz && \
    tar xzf v${FDKAAC_VERSION}.tar.gz && \
    cd fdk-aac-${FDKAAC_VERSION} && \
    autoreconf -fiv && ./configure --enable-static --disable-shared && \
    make -j"$(nproc)" && make install

# 编译 FFmpeg
RUN wget -q https://ffmpeg.org/releases/ffmpeg-${FFMPEG_VERSION}.tar.gz && \
    tar xzf ffmpeg-${FFMPEG_VERSION}.tar.gz && \
    cd ffmpeg-${FFMPEG_VERSION} && \
    ./configure \
      --pkg-config-flags="--static" \
      --extra-cflags="-I/usr/local/include" \
      --extra-ldflags="-L/usr/local/lib" \
      --extra-libs="-lpthread -lm" \
      --enable-gpl \
      --enable-nonfree \
      --enable-libfdk_aac \
      --disable-shared \
      --enable-static && \
    make -j"$(nproc)" && make install && \
    strip /usr/local/bin/ffmpeg /usr/local/bin/ffprobe && \
    rm -rf /tmp/build 

# 编译 GPAC（MP4Box）
RUN git clone --depth=1 https://github.com/gpac/gpac.git /tmp/gpac && \
    cd /tmp/gpac && \
    ./configure --static-bin --disable-x11 --disable-pulseaudio --disable-oss-audio && \
    make -j"$(nproc)" && make install && \
    strip /usr/local/bin/MP4Box && \
    rm -rf /tmp/gpac /tmp/build


# ==============================
# 第二阶段：生产镜像
# ==============================
FROM python:3.12-slim

# ---- 设置环境变量 ----
ENV TZ=Asia/Shanghai \
    DEBIAN_FRONTEND=noninteractive \
    PATH="/root/.deno/bin:/app/data/bin:${PATH}"

# ---- 安装运行依赖 ----
RUN apt-get update && apt-get install -y --no-install-recommends \
    tzdata \
    ca-certificates  \
    curl \
    tar \
    unzip \
    libicu-dev \
    inetutils-telnet \
 && ln -snf /usr/share/zoneinfo/Asia/Shanghai /etc/localtime \
 && echo Asia/Shanghai > /etc/timezone \
 && python3 -m pip install --upgrade pip setuptools wheel \
 && apt-get clean && rm -rf /var/lib/apt/lists/*

# ---- 安装 Deno ----
RUN curl -fsSL https://deno.land/install.sh | sh && \
    rm -rf /root/.cache/deno

# ---- 安装 N_m3u8DL-RE ----
RUN mkdir -p /usr/local/bin && \
    curl -sL https://github.com/nilaoda/N_m3u8DL-RE/releases/download/v0.3.0-beta/N_m3u8DL-RE_v0.3.0-beta_linux-x64_20241203.tar.gz \
      | tar -xz -C /usr/local/bin && \
    chmod +x /usr/local/bin/N_m3u8DL-RE 

# 安装 Playwright
RUN pip install playwright && playwright install chromium

# 拷贝 FFmpeg + MP4Box 二进制
COPY --from=ffmpeg-builder /usr/local/bin/ffmpeg /usr/local/bin/ffprobe /usr/local/bin/MP4Box /usr/local/bin/