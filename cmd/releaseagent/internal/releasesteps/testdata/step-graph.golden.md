---
config:
  layout: elk
---
flowchart RL
  0(Create release day issue)
  1(Sync, 1.22.10-1) --> 0
  2(⌚ Wait for PR merge, 1.22.10-1) --> 1
  3(⌚ Wait for AzDO sync, 1.22.10-1) --> 2
  4(🚀 Trigger official build, 1.22.10-1) --> 3
  5(⌚ Wait for official build, 1.22.10-1) --> 4
  6(🚀 Trigger innerloop build, 1.22.10-1) --> 3
  7(⌚ Wait for innerloop build, 1.22.10-1) --> 6
  8(✅ Artifacts ok to publish, 1.22.10-1) --> 5 & 7
  9(🚀 Trigger Azure Linux PR creation, 1.22.10-1) --> 8
  10(✅ External publish complete, 1.22.10-1) --> 9
  11(Sync, 1.23.4-1) --> 0
  12(⌚ Wait for PR merge, 1.23.4-1) --> 11
  13(⌚ Wait for AzDO sync, 1.23.4-1) --> 12
  14(🚀 Trigger official build, 1.23.4-1) --> 13
  15(⌚ Wait for official build, 1.23.4-1) --> 14
  16(🚀 Trigger innerloop build, 1.23.4-1) --> 13
  17(⌚ Wait for innerloop build, 1.23.4-1) --> 16
  18(✅ Artifacts ok to publish, 1.23.4-1) --> 15 & 17
  19(🚀 Trigger Azure Linux PR creation, 1.23.4-1) --> 18
  20(✅ External publish complete, 1.23.4-1) --> 19
  21(Download asset metadata, 1.22.10-1) --> 5
  22(Download artifacts, 1.22.10-1) --> 5
  23(🎓 Create GitHub tag, 1.22.10-1) --> 8
  24(🎓 Create GitHub release, 1.22.10-1) --> 21 & 22 & 23
  25(🎓 Update aka.ms links, 1.22.10-1) --> 8 & 21
  26(Update Dockerfiles, 1.22.10-1) --> 8 & 21
  27(✅ microsoft/go publish and go-images PR complete, 1.22.10-1) --> 24 & 25 & 26
  28(Download asset metadata, 1.23.4-1) --> 15
  29(Download artifacts, 1.23.4-1) --> 15
  30(🎓 Create GitHub tag, 1.23.4-1) --> 18
  31(🎓 Create GitHub release, 1.23.4-1) --> 28 & 29 & 30
  32(🎓 Update aka.ms links, 1.23.4-1) --> 18 & 28
  33(Update Dockerfiles, 1.23.4-1) --> 18 & 28
  34(✅ microsoft/go publish and go-images PR complete, 1.23.4-1) --> 31 & 32 & 33
  35(✅ All microsoft/go publish and go-images PRs complete) --> 27 & 34
  36(Get go-images commit) --> 35
  37(🚀 Trigger go-image build/publish) --> 36
  38(⌚ Wait for go-image build/publish) --> 37
  39(🌊 Check published image version) --> 38
  40(📰 Create blog post markdown) --> 35 & 39
  41(✅ Complete) --> 10 & 20 & 39 & 40

%% https://mermaid.live/view#pako:eNqclk+O2zYUh6/ywJUC2BORlP/uBuMiXaRokbYoUGhDS5RMiCINiupkJgjQTTdF0E12QYFcoWfKCXKEgrSlsUaW7HbHgfi9R/P36Y3eoUSnHK3RdDqNVaJVJvJ1rAAke9C1XQOXRaz8w0zq+2THjIU3r92OMLgznFkOhkvOKg4pewBRVTV/4R7j4McHlUwA3xByg8MpfgHTaVyHIeUQug0k+PLhE/zChIVMG/jhDZTc5Pw8gR1Bu8Tt4+Z7qAabEIdEwdfPn36Hn4zIc25AZ5lIBJOwrYVMz3PUcbNuq2u4yHHzbj+hFDdS6/3Fhotuw6vAuQOXwZe//4BbY0XGEluBLsBq2NdbKardeW4GbkHmsHAFVt0j3z7WhsNroeq3LpPEZSy0Ol9p6ZMO/RG+eWu5UUw2vSHR5V5yO5DoyqMnltCbqC8JHrPkOYG9JXhUkx7jNcGXPemBPjZ8WZQe6EXBV5gy0PIKVXqkVwVfdqUHtq5gLwv+D7b0anlbyDW29FBvC8HBRt8rqVkKrKq4hZJbljLLBjz3EDmBml8+tp8GXz//9RGOw+2VsN/WW7AsH3kDSHSOOc7FgemEm4slpF35fMnsUOznfeqKsYLdlBVIoYqBYy9b3vtP5sER3eik4CYTkl9JLnw0pUiMrnRmX+atHMBUCrmeipLlvPJRj77dJGpLtwYRLyFZjobYF9BDq6EQz++n4ViI58Wk+FKIzznydH+rZkX92KJkPMP+CdpKh7PQoQwvktH/zfB5ZdoaSltDqTeUzg5zRMrr+lRto9PLW7RF/UCk8+AVtydcostS2NMDHaJddCdQAxym38vjGU4xrx1ddqfmZczPO+rm3Yc/4W7Hk6L5gTyFA/sbN5XQ6pTyIUROv4//NCptpc5hrysLJTNFqu87RPt+UD/kIuwv965/Yzhsow6fmGYVhbFCE1RyUzKRojV656rFyO54yWO0hhilzBQxitV7NEG112sjWG5YidbW1HyCWG21+3/c/P2odYnWeIJ4Kqw23x0+Ff0X4wTtmfrVP3d73/8bAAD///DGFgs=
