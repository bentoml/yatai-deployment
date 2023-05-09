FROM debian:stable-slim as fetcher
COPY fetch_binaries.sh /tmp/fetch_binaries.sh

RUN apt-get update && apt-get install -y \
  curl \
  wget

RUN /tmp/fetch_binaries.sh

FROM python:3.11.0-slim-bullseye

RUN apt-get update
RUN apt-get install -y vim jq fping curl iftop mtr dnsutils ldnsutils procps htop net-tools iproute2 ipset iptraf-ng tcpdump tcptraceroute strace sysstat iperf iperf3 ngrep zsh wget git
RUN pip install --no-cache-dir ipython ipdb py-spy httpie

RUN apt-get install -y iputils-ping netcat

# Installing ctop - top-like container monitor
COPY --from=fetcher /tmp/ctop /usr/local/bin/ctop

# Installing calicoctl
COPY --from=fetcher /tmp/calicoctl /usr/local/bin/calicoctl

# Installing termshark
COPY --from=fetcher /tmp/termshark /usr/local/bin/termshark

USER root
WORKDIR /root
ENV HOSTNAME bento-debugger

# ZSH Themes
RUN wget https://github.com/robbyrussell/oh-my-zsh/raw/master/tools/install.sh -O - | zsh || true
RUN git clone https://github.com/zsh-users/zsh-syntax-highlighting.git ${ZSH_CUSTOM:-~/.oh-my-zsh/custom}/plugins/zsh-syntax-highlighting
RUN git clone https://github.com/zsh-users/zsh-autosuggestions ${ZSH_CUSTOM:-~/.oh-my-zsh/custom}/plugins/zsh-autosuggestions
RUN git clone --depth=1 https://github.com/romkatv/powerlevel10k.git ${ZSH_CUSTOM:-$HOME/.oh-my-zsh/custom}/themes/powerlevel10k
COPY zshrc .zshrc
COPY motd motd

# Running ZSH
CMD ["zsh"]
