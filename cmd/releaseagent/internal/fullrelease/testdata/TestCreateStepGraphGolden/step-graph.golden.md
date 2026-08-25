---
config:
  layout: elk
---
flowchart RL
  0(Create release day issue)
  1(⌚ Get upstream commit for release, 1.22.10-1) --> 0
  2(Create sync PR, 1.22.10-1) --> 1
  3(⌚ Wait for PR merge, 1.22.10-1) --> 2
  4(⌚ Wait for AzDO sync, 1.22.10-1) --> 3
  5(🚀 Trigger official build, 1.22.10-1) --> 4
  6(⌚ Wait for official build, 1.22.10-1) --> 5
  7(🚀 Trigger innerloop build, 1.22.10-1) --> 4
  8(⌚ Wait for innerloop build, 1.22.10-1) --> 7
  9(✅ Artifacts ok to publish, 1.22.10-1) --> 6 & 8
  10(🚀 Trigger Azure Linux PR creation, 1.22.10-1) --> 9
  11(✅ External publish complete, 1.22.10-1) --> 10
  12(⌚ Get upstream commit for release, 1.23.4-1) --> 0
  13(Create sync PR, 1.23.4-1) --> 12
  14(⌚ Wait for PR merge, 1.23.4-1) --> 13
  15(⌚ Wait for AzDO sync, 1.23.4-1) --> 14
  16(🚀 Trigger official build, 1.23.4-1) --> 15
  17(⌚ Wait for official build, 1.23.4-1) --> 16
  18(🚀 Trigger innerloop build, 1.23.4-1) --> 15
  19(⌚ Wait for innerloop build, 1.23.4-1) --> 18
  20(✅ Artifacts ok to publish, 1.23.4-1) --> 17 & 19
  21(🚀 Trigger Azure Linux PR creation, 1.23.4-1) --> 20
  22(✅ External publish complete, 1.23.4-1) --> 21
  23(Download asset metadata, 1.22.10-1) --> 6
  24(Download artifacts, 1.22.10-1) --> 6
  25(🎓 Create GitHub tag, 1.22.10-1) --> 9
  26(🎓 Create GitHub release, 1.22.10-1) --> 23 & 24 & 25
  27(🎓 Update aka.ms links, 1.22.10-1) --> 9 & 23
  28(Update Dockerfiles, 1.22.10-1) --> 9 & 23
  29(✅ microsoft/go publish and go-images PR complete, 1.22.10-1) --> 26 & 27 & 28
  30(Download asset metadata, 1.23.4-1) --> 17
  31(Download artifacts, 1.23.4-1) --> 17
  32(🎓 Create GitHub tag, 1.23.4-1) --> 20
  33(🎓 Create GitHub release, 1.23.4-1) --> 30 & 31 & 32
  34(🎓 Update aka.ms links, 1.23.4-1) --> 20 & 30
  35(Update Dockerfiles, 1.23.4-1) --> 20 & 30
  36(✅ microsoft/go publish and go-images PR complete, 1.23.4-1) --> 33 & 34 & 35
  37(✅ All microsoft/go publish and go-images PRs complete) --> 29 & 36
  38(Get go-images commit) --> 37
  39(🚀 Trigger go-image build/publish) --> 38
  40(⌚ Wait for go-image build/publish) --> 39
  41(🌊 Check published image version) --> 40
  42(📰 Create blog post markdown) --> 37 & 41
  43(✅ Complete) --> 11 & 22 & 41 & 42

%% https://mermaid.live/view#pako:eNqclk9u4zYUh6/ywJUHsDMiqfjfLkiK6WKKDtIWBQptGIlWCFOkQVHNJIMBuummGHQzu0GBXqFnmhP0CAWfLMWOLMnoTob4vUfq9+lZH0hqM0nWZDabJSa1ZqPydWIAtHi0lV+D1NvE4M2Ntg/pvXAebt+GFdHk2knhJTippSglZOIRVFlW8lW4TSdfP32BN9JDtSu9k6KA1BaF8rCxrmGmQC8Yu6DRjL6C2SypoohLiALPmvLlo0nh3e3ppTQs5djqZ7Gv/e4WCunynuIsEPExcfV08z32OY3wgFxO/v37y2/wo1N5Lh3YzUalSmi4q5TOTnNx4ObHrc7hLgO3OO6njJFOW7sbbbg8bngWuAjgavL1r9/hynm1EakvwW7BW9hVd1qV96e5OYQLNoclRh4d7/nqqXIS3ipTvQ+hpCFPZc3pUiusQHEP37z30hmhm+bBnJ2WvidSisJQdrZx/CLuCkf5KeNeLqXoD40HlOsg6A+9HHKuw2CUdD4qXQdEeehi1LoOOEdwOa5dT8vVuHcdErVh0ah4HXDRiEfRG0bPN+9lLVaPG3aGeR0Uxw/jkxv7YLQVGYiylB4K6UUmvOh5aRCKD6Dm5EPrw/z58zPsFX2j/LfVHXiRD7xNbH6KGRy9jDcPlsXtFebLFnWxn3ZZKCa24qIoQSuz7dn2quXRf7ac7NEbm26l2ygtzyTrwVSo1NnSbvzrvJUDhMkgtzNViFyWGPXgpGDtwGKtQQwl5NFgiF0BEaJ9IfasZ0MhnhaT87EQX3I8ao7GaXuFY4vHwxl2d9Dy9V4u+zIcJef/N8PO6VpDeWsoR0P5op4jWp/Xp2wbHW68lY/je8eXk/CP8szVfymHG6qjXR1PoAaop9/r/R4OMdQujo6n5jiGb3Yc5t2nP+D6Xqbb5oAyg5r9VbpSWXP4YYAhxEG/z/80Kt1pm8POlh4K4baZfTBHp2qeQ4xDLub4cK+7T4y2kjH2zLRXLDFkSgrpCqEysiYfQrWE+HtZyISsISGZcNuEJOYjmZIK9bpRIneiIGvvKjklovL2h0eTNr+frC3Imk6JzJS37rv6CxY/ZKdkJ8wveD+s/fhfAAAA//+4hEVT
