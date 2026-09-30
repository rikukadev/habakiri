class Comment < ApplicationRecord
  belongs_to :author,
             class_name: "User",
             foreign_key: "author_id"
  belongs_to :commentable, polymorphic: true
  belongs_to :access_grant,
             class_name: "Doorkeeper::AccessGrant",
             optional: true
  belongs_to :reviewer, class_name: "Moderation::Reviewer", optional: true
end
