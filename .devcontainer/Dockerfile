FROM nvcr.io/nvidia/cuda:13.1.1-base-ubuntu22.04
ARG GOLANG_VERSION=1.24.12
ARG USERNAME=developer
ARG USER_UID=1000
ARG USER_GID=1000
# Create a user 'developer' with UID=1000, add to 'developer' group, and add to 'sudo' group
RUN groupadd -g $USER_GID $USERNAME && \
	useradd -m -u $USER_GID -g $USERNAME -s /bin/bash $USERNAME && \
	usermod -aG sudo $USERNAME
# Allow 'developer' to use sudo without a password
RUN echo "$USERNAME ALL=(ALL) NOPASSWD:ALL" >> /etc/sudoers

RUN --mount=type=cache,target=/var/cache/apt \
	set -eux; \
	apt-get update; \
	apt-get install -y --no-install-recommends \
	git \
	ca-certificates \
	curl \
	g++ \
	gcc \
	libc6-dev \
	make \
	pkg-config \
	wget \
	datacenter-gpu-manager-4-core \
	libcap2-bin \
	&& install -m 0755 -d /etc/apt/keyrings \
	&& wget -O /etc/apt/keyrings/docker.asc https://download.docker.com/linux/ubuntu/gpg \
	&& chmod a+r /etc/apt/keyrings/docker.asc \
	&& echo \
	"deb [arch=$(dpkg --print-architecture) signed-by=/etc/apt/keyrings/docker.asc] https://download.docker.com/linux/ubuntu \
	$(. /etc/os-release && echo "$VERSION_CODENAME") stable" | \
	tee /etc/apt/sources.list.d/docker.list > /dev/null \
	&& apt-get update \
	&& apt-get install -y --no-install-recommends docker-ce docker-ce-cli containerd.io docker-buildx-plugin \
	&& apt-get autoremove -y \
	&& rm -rfd /usr/local/dcgm/bindings /usr/local/dcgm/sdk_samples /usr/share/nvidia-validation-suite \
	# DCGM exporter doesn't use libdcgm_cublas_proxy*.so.
	&& rm -rf /usr/lib/x86_64-linux-gnu/libdcgm_cublas_proxy*.so \
	&& rm -rf /var/lib/apt/lists/*

RUN set -eux; \
	arch="$(dpkg --print-architecture)"; arch="${arch##*-}"; \
	url=; \
	filename=; \
	echo "$arch"; \
	case "$arch" in \
	'amd64') \
	filename="go${GOLANG_VERSION}.linux-amd64.tar.gz"; \
	url="https://dl.google.com/go/${filename}"; \
	;; \
	'arm64') \
	filename="go${GOLANG_VERSION}.linux-arm64.tar.gz"; \
	url="https://dl.google.com/go/${filename}"; \
	;; \
	*) echo >&2 "error: unsupported architecture '$arch' (likely packaging update needed)"; exit 1 ;; \
	esac; \
	build=; \
	if [ -z "$url" ]; then \
	# https://github.com/golang/go/issues/38536#issuecomment-616897960
	build=1; \
	filename="go${GOLANG_VERSION}.src.tar.gz"; \
	url="https://dl.google.com/go/${filename}"; \
	echo >&2; \
	echo >&2 "warning: current architecture ($arch) does not have a compatible Go binary release; will be building from source"; \
	echo >&2; \
	fi; \
	wget -O go.tgz "$url" --progress=dot:giga; \
	\
	echo "Verifying SHA256 checksum..."; \
	wget -O go.sha256 "https://dl.google.com/go/${filename}.sha256"; \
	expected_sha256=$(cat go.sha256); \
	actual_sha256=$(sha256sum go.tgz | awk '{print $1}'); \
	if [ "$expected_sha256" != "$actual_sha256" ]; then \
		echo >&2 "error: SHA256 checksum verification failed"; \
		echo >&2 "expected: $expected_sha256"; \
		echo >&2 "actual:   $actual_sha256"; \
		exit 1; \
	fi; \
	echo "SHA256 checksum verified successfully"; \
	rm go.sha256; \
	\
	tar -C /usr/local -xzf go.tgz; \
	rm go.tgz
ENV GOTOOLCHAIN=local
ENV GOPATH=/go
ENV PATH=$GOPATH/bin:$PATH
RUN mkdir -p "$GOPATH/src" "$GOPATH/bin" && chmod -R 1777 "$GOPATH"
ENV PATH=$PATH:/usr/local/go/bin

# Required for DCGM metrics
ENV NVIDIA_DRIVER_CAPABILITIES=compute,utility,compat32
# disable all constraints on the configurations required by NVIDIA container toolkit
ENV NVIDIA_DISABLE_REQUIRE="true"
ENV NVIDIA_VISIBLE_DEVICES=all
