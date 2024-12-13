golang: bump Go version to 1.23.3-2

---

Hi! 👋 I'm the Microsoft Go team's bot. This is an automated pull request I generated to bump the Go version to [1.23.3-2](https://github.com/microsoft/go/releases/tag/v1.23.3-2).

I'm not able to run the Azure Linux pipelines yet, so the Microsoft Go release runner will need to finalize this PR. @a-go-developer

Finalization steps:
- Trigger [Source Tarball Publishing](https://dev.azure.com/mariner-org/mariner/_build?definitionId=2284) with:  
  Full Name:  
  ```
  go1.23.3-20240708.2.src.tar.gz
  ```
  URL:  
  ```
  https://github.com/microsoft/go/releases/download/v1.23.3-2/go1.23.3-20240708.2.src.tar.gz
  ```
- Trigger [the Buddy Build](https://dev.azure.com/mariner-org/mariner/_build?definitionId=2190) with:  
  First field: `PR-` then the number of this PR.  
  Core spec:  
  ```
  golang-1.23
  ```
- Post a PR comment with the URL of the triggered Buddy Build.
- Mark this draft PR as ready for review.

Thanks!
