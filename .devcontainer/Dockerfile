FROM nvcr.io/nvidia/cuda:13.4.3-base-ubuntu26.04@sha256:e2eb43e25d404e2c48716f80afdb8989a84c4da588f5509330322842084d97b0
ARG DCGM_VERSION=4.7.0
# This is the local development account name, not a credential.
# hadolint ignore=DL3064
ARG USERNAME=developer
ARG USER_UID=1000
ARG USER_GID=1000
SHELL ["/bin/bash", "-o", "pipefail", "-c"]
# Create or reuse a UID=1000 user named 'developer', and add it to 'sudo'.
RUN set -eux; \
	getent group $USER_GID || groupadd -g $USER_GID $USERNAME; \
	existing_user="$(getent passwd $USER_UID | cut -d: -f1 || true)"; \
	if [ -n "$existing_user" ]; then \
		if [ "$existing_user" != "$USERNAME" ] && id -u $USERNAME >/dev/null 2>&1; then userdel -r $USERNAME || true; fi; \
		if [ "$existing_user" != "$USERNAME" ]; then usermod -l $USERNAME -d /home/$USERNAME -m "$existing_user"; fi; \
		usermod -g $USER_GID $USERNAME; \
	elif id -u $USERNAME >/dev/null 2>&1; then \
		usermod -u $USER_UID -g $USER_GID $USERNAME; \
	else \
	useradd -m -l -u $USER_UID -g $USER_GID -s /bin/bash $USERNAME; \
	fi; \
	usermod -aG sudo $USERNAME
# Allow 'developer' to use sudo without a password
RUN echo "$USERNAME ALL=(ALL) NOPASSWD:ALL" >> /etc/sudoers

# /etc/os-release is provided by the Ubuntu base image at build time.
# hadolint ignore=SC1091
RUN --mount=type=cache,target=/var/cache/apt \
	set -eux; \
	apt-get update; \
	DCGM_PACKAGE_VERSION="1:${DCGM_VERSION}-2"; \
	# APT prefers newer split-package builds over the requested DCGM build.
	# Remove this pin when APT can resolve the requested build without it.
	printf '%s\n' \
	    'Package: datacenter-gpu-manager-4* libdcgm dcgmi nv-hostengine' \
	    "Pin: version ${DCGM_PACKAGE_VERSION}" \
	    'Pin-Priority: 1000' \
	    > /etc/apt/preferences.d/dcgm; \
	apt-get install -y --no-install-recommends \
	git \
	ca-certificates \
	curl \
	file \
	g++ \
	gcc \
	jq \
	libc6-dev \
	pkg-config \
	python3 \
	ripgrep \
	ruby \
	wget \
	"datacenter-gpu-manager-4-core=${DCGM_PACKAGE_VERSION}" \
	"datacenter-gpu-manager-4-dev=${DCGM_PACKAGE_VERSION}" \
	"datacenter-gpu-manager-4-proprietary=${DCGM_PACKAGE_VERSION}" \
	libcap2-bin \
	&& rm /etc/apt/preferences.d/dcgm \
	&& install -m 0755 -d /etc/apt/keyrings \
	&& wget --progress=dot:giga -O /etc/apt/keyrings/docker.asc https://download.docker.com/linux/ubuntu/gpg \
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
	&& rm -rf /usr/lib/*-linux-gnu/libdcgm_cublas_proxy*.so \
	&& rm -rf /var/lib/apt/lists/*

COPY .bazelversion bootstrap.sh mise.toml mise.lock Taskfile.yml /opt/dcgm-exporter/
COPY hack/mise-bootstrap.env hack/versions.env /opt/dcgm-exporter/hack/
COPY hack/task /opt/dcgm-exporter/hack/task/
ENV MISE_DATA_DIR=/opt/mise \
    PATH=/opt/mise/bin:/opt/mise/shims:${PATH}
RUN set -eux; \
	/opt/dcgm-exporter/bootstrap.sh --bin-dir /usr/local/bin; \
	task --dir /opt/dcgm-exporter deps:install; \
	mise --cd /opt/dcgm-exporter reshim; \
	task --dir /opt/dcgm-exporter deps:check; \
	chmod -R a+rX /opt/dcgm-exporter "${MISE_DATA_DIR}"

# Required for DCGM metrics
ENV NVIDIA_DRIVER_CAPABILITIES=compute,utility,compat32
# disable all constraints on the configurations required by NVIDIA container toolkit
ENV NVIDIA_DISABLE_REQUIRE="true"
ENV NVIDIA_VISIBLE_DEVICES=all
