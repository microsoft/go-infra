Completed building all images! Before you [announce](https://github.com/microsoft/go-infra/blob/main/docs/release-process/instructions.md#making-the-internal-announcement), confirm the MAR/MCR images are updated using commands like these:

```
image=mcr.microsoft.com/oss/go/microsoft/golang:1-bullseye
docker pull $image
docker run -it --rm $image go version
```
