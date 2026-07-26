# TypeScript/JavaScript-flavored sandbox variant (roadmap step 8): adds the
# TypeScript compiler + typescript-language-server and the official
# typescript-lsp Claude Code plugin on top of the base masuda-loop image.
# Like the Python variant, this rides on the Node.js already present in the
# base image -- no new system toolchain needed.
FROM masuda-loop:latest

# npm's global install path (/usr/lib/node_modules) is root-owned; the base
# image's last USER directive is ubuntu, so switch back for this one step.
USER root
RUN npm install -g typescript typescript-language-server

USER ubuntu
RUN claude plugin install typescript-lsp@claude-plugins-official --scope user
