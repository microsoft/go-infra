# Copyright (c) Microsoft Corporation.
# Use of this source code is governed by a BSD-style
# license that can be found in the LICENSE file.

# Pack up the repo into morepath.pack
git -C moremath bundle create ../moremath.pack --all
. .\clean.ps1
