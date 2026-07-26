# Python-flavored sandbox variant (roadmap step 8): adds pyright and the
# official pyright-lsp Claude Code plugin on top of the base masuda-loop
# image. Unlike Go, this needs no new system toolchain -- pyright ships as
# an npm package, and Node.js is already in the base image (it's how
# @anthropic-ai/claude-code itself gets installed), so the incremental size
# here is small.
FROM masuda-loop:latest

# npm's global install path (/usr/lib/node_modules) is root-owned; the base
# image's last USER directive is ubuntu, so switch back for this one step.
USER root
RUN npm install -g pyright

USER ubuntu
RUN claude plugin install pyright-lsp@claude-plugins-official --scope user
