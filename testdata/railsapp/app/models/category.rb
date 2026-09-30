class Category < ApplicationRecord
  self.table_name = "taxonomy"
  belongs_to :parent, class_name: "Category", optional: true

  def detach_posts!
    # コールバックを通らない一括書き込み
    Post.where(category_id: id).update_all(category_id: nil)
  end

  def self.comment_stats
    connection.select_all("SELECT count(*) FROM posts JOIN comments ON comments.commentable_id = posts.id")
  end

  def refresh_counters!
    Post.where(category_id: id).count
  end
end
