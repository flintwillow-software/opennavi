name: Pull Request
description: Open a pull request
body:
  - type: markdown
    attributes:
      value: |
        ### Before submitting
        - [ ] I've read the [CONTRIBUTING.md](./CONTRIBUTING.md)
        - [ ] My changes follow the [conventions](../docs/conventions.md)
  - type: dropdown
    id: type
    attributes:
      label: Type of change
      description: What kind of change is this?
      options:
        - Bug fix
        - New feature
        - Breaking change
        - Documentation update
        - Refactor
        - Test
        - Chore
      default: 0
    validations:
      required: true
  - type: input
    id: related-issue
    attributes:
      label: Related issue / epic
      description: Link to any related issue or epic
      placeholder: "#123"
  - type: textarea
    id: description
    attributes:
      label: Description
      description: Describe your changes in detail
      placeholder: e.g., This PR fixes the auth timeout issue by...
    validations:
      required: true
  - type: textarea
    id: testing
    attributes:
      label: Testing
      description: How did you verify this change?
      placeholder: |
        - [ ] Unit tests pass
        - [ ] Manual testing steps:
          1. 
          2. 
    validations:
      required: true
  - type: textarea
    id: screenshots
    attributes:
      label: Screenshots (if applicable)
      description: Drag and drop or paste screenshots
  - type: textarea
    id: checklist
    attributes:
      label: Checklist
      description: Ensure all requirements are met
      value: |
        - [ ] My code follows the style guidelines of this project
        - [ ] I have performed a self-review of my own code
        - [ ] I have commented my code, particularly in hard-to-understand areas
        - [ ] I have made corresponding changes to the documentation
        - [ ] My changes generate no new warnings
        - [ ] I have added tests that prove my fix is effective or that my feature works
        - [ ] New and existing unit tests pass locally with my changes
        - [ ] Any dependent changes have been merged and published
